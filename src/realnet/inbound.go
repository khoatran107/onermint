package realnet

import (
	"context"
	"errors"
	"fmt"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/network"
	pb "blockchain-lab01/src/proto"
	"blockchain-lab01/src/types"
)

var errNodeStopping = errors.New("node stopping")

// ownerCall hands r to the single owner loop and waits for its reply on
// reply. stopping is returned if the node shuts down (nil: the node's ctx error).
func ownerCall[T any](p *process, ctx context.Context, r received, reply <-chan T, stopping error) (T, error) {
	var zero T
	stopped := func() error {
		if stopping != nil {
			return stopping
		}
		return p.ctx.Err()
	}
	select {
	case p.in <- r:
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.ctx.Done():
		return zero, stopped()
	}
	select {
	case v := <-reply:
		return v, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.ctx.Done():
		return zero, stopped()
	}
}

func (p *process) acceptFrame(ctx context.Context, e *pb.Envelope) error {
	if err := VerifyEnvelope(p.cfg.ChainID, p.cfg.NodeID, p.ids, e); err != nil {
		p.event(int(e.GetSender()), network.Event{Event: "reject_transport", Reason: err.Error()})
		return err
	}
	return p.enqueueVerified(ctx, e)
}

// Called only after transport authentication, either by acceptFrame or the
// GossipSub topic validator. Both paths still execute all protocol validation.
func (p *process) enqueueVerified(ctx context.Context, e *pb.Envelope) error {
	r := received{frame: e, done: make(chan error, 1)}
	result, err := ownerCall(p, ctx, r, r.done, nil)
	if err != nil {
		return err
	}
	return result
}

// AcceptHeader records that the runtime accepted a peer's header and requests
// the matching body; only then may that body be received.
func (p *process) AcceptHeader(peer int, ref types.Hash, h, r uint64) error {
	k := transfer{peer, ref}
	if !p.seen[k] {
		return fmt.Errorf("real header not received")
	}
	p.accepted[k] = true
	p.event(peer, network.Event{Event: "header_accept", Type: network.Header, Height: h, Round: r, Ref: ref})
	return p.Send(peer, network.Message{Kind: network.Request, Ref: ref, Height: h, Round: r})
}

// receive applies per-peer rate limiting and the header-before-body rule, then
// passes the frame to the runtime. Runs on the owner loop.
func (p *process) receive(e *pb.Envelope) error {
	if !p.started {
		return network.Reject(fmt.Errorf("node awaiting experiment start"))
	}
	peer := int(e.Sender)
	if limited, err := p.rateLimit(peer, e); limited || err != nil {
		return err
	}
	ref := refOf(e)
	k := transfer{peer, ref}
	kind := network.Kind(e.Kind)
	switch kind {
	case network.Header:
		if crypt.HashBytes(e.Payload) != ref {
			return network.Reject(fmt.Errorf("wire header reference mismatch"))
		}
		p.seen[k] = true
	case network.Request:
		p.requested[k] = true
	case network.Body:
		if !p.accepted[k] {
			return network.Reject(fmt.Errorf("wire body without accepted header"))
		}
	}
	p.event(peer, network.Event{Event: "receive", Type: kind, Height: e.Height, Round: e.Round, Ref: ref, Packet: e.Sequence})
	return p.runtime.Receive(network.Packet{Message: network.Message{Kind: kind, Ref: ref, Height: e.Height, Round: e.Round, Data: append([]byte(nil), e.Payload...)}, ID: e.Sequence, From: peer, To: p.cfg.NodeID})
}

// rateLimit drops frames from a temporarily blocked peer and blocks a peer
// that exceeds ReceiveLimit frames per ReceiveWindowMS for BlockMS.
func (p *process) rateLimit(peer int, e *pb.Envelope) (bool, error) {
	now := p.now()
	if until := p.blocked[peer]; until > now {
		p.frameEvent(e, "peer_drop", "temporarily blocked")
		return true, nil
	}
	a := p.arrivals[peer]
	window := now / p.cfg.ReceiveWindowMS
	if window != a.window {
		a = arrival{window: window}
	}
	a.count++
	p.arrivals[peer] = a
	if a.count <= p.cfg.ReceiveLimit {
		return false, nil
	}
	until := now + p.cfg.BlockMS
	p.blocked[peer] = until
	p.event(peer, network.Event{Event: "block", Height: e.Height, Round: e.Round, Reason: "peer rate exceeded"})
	return true, p.Schedule(p.cfg.BlockMS, func() error {
		if p.blocked[peer] == until {
			delete(p.blocked, peer)
			p.event(peer, network.Event{Event: "unblock", Height: e.Height, Round: e.Round})
		}
		return nil
	})
}
