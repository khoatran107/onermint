package realnet

import (
	"fmt"
	"sync"
	"time"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/network"
	pb "blockchain-lab01/src/proto"
)

// Send signs one protocol message, runs it through the fault layer and queues
// every planned copy for transmission. Called only from the owner loop.
func (p *process) Send(peer int, m network.Message) error {
	if peer < network.Broadcast || peer >= len(p.ids) || peer == p.cfg.NodeID {
		return fmt.Errorf("invalid real peer")
	}
	if peer == network.Broadcast && !m.Kind.Broadcastable() {
		return fmt.Errorf("only headers and votes use GossipSub broadcast")
	}
	k := transfer{peer, m.Ref}
	if m.Kind == network.Header && crypt.HashBytes(m.Data) != m.Ref {
		return fmt.Errorf("header reference mismatch")
	}
	if m.Kind == network.Request && !p.accepted[k] {
		return fmt.Errorf("request without accepted header")
	}
	if m.Kind == network.Body && !p.requested[k] {
		return fmt.Errorf("body without acceptance request")
	}
	if p.queued.Load() >= int64(p.cfg.QueueLimit) {
		p.event(peer, network.Event{Event: "queue_full", Type: m.Kind, Height: m.Height, Round: m.Round})
		return network.ErrQueueFull
	}
	p.packet++
	e := &pb.Envelope{ChainId: p.cfg.ChainID, Sender: uint32(p.cfg.NodeID), Recipient: uint32(peer), Sequence: p.packet, Kind: string(m.Kind), Reference: append([]byte(nil), m.Ref[:]...), Height: m.Height, Round: m.Round, Payload: append([]byte(nil), m.Data...)}
	if err := p.signEnvelope(e); err != nil {
		return err
	}
	plans, dropped := p.faults.plan(p.faultClock(), e)
	if p.queued.Load()+int64(len(plans)) > int64(p.cfg.QueueLimit) {
		p.frameEvent(e, "queue_full", "fault copies exceed remaining outbound capacity")
		return network.ErrQueueFull
	}
	if dropped {
		p.frameEvent(e, "fault_drop", "")
	}
	for _, d := range plans {
		p.queued.Add(1)
		p.frameEvent(d.frame, "fault_"+d.reason, fmt.Sprintf("hold %d ms", d.after))
		if err := p.Schedule(d.after, func() error {
			select {
			case p.jobs <- d.frame:
			default:
				p.queued.Add(-1)
				p.frameEvent(d.frame, "queue_full", "transmit queue full; peer retry required")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// faultClock is the time the fault policy sees: since the common experiment
// start when configured, otherwise since this process started.
func (p *process) faultClock() uint64 {
	if p.cfg.StartAtUnixMS <= 0 {
		return p.now()
	}
	return uint64(max(time.Now().UnixMilli()-p.cfg.StartAtUnixMS, 0))
}

func (p *process) frameEvent(e *pb.Envelope, kind, reason string) {
	p.Record(network.Event{Node: int(e.Sender), Peer: peerOf(e.Recipient), Event: kind, Type: network.Kind(e.Kind), Height: e.Height, Round: e.Round, Ref: refOf(e), Packet: e.Sequence, Reason: reason})
}

// Each peer has independent IO. A shared send gate preserves the node-wide
// rate at actual transmission time without holding the gate during an RPC.
func (p *process) sender(wg *sync.WaitGroup) {
	defer wg.Done()
	var workers sync.WaitGroup
	queues := make([]chan *pb.Envelope, len(p.ids))
	for peer := range p.ids {
		if peer == p.cfg.NodeID {
			continue
		}
		jobs := make(chan *pb.Envelope, p.cfg.QueueLimit)
		queues[peer] = jobs
		workers.Add(1)
		go p.sendPeer(peer, jobs, &workers)
	}
	defer workers.Wait()
	for {
		select {
		case <-p.ctx.Done():
			return
		case e := <-p.jobs:
			if e.Recipient == broadcastRecipient {
				if err := p.transmit(e, func() error { return p.gossip.publish(e) }); err != nil && p.ctx.Err() == nil {
					p.frameEvent(e, "send_error", err.Error())
				}
				p.queued.Add(-1)
				continue
			}
			select {
			case queues[int(e.Recipient)] <- e:
			default:
				p.queued.Add(-1)
				p.frameEvent(e, "queue_full", "peer transmit queue full; retry required")
			}
		}
	}
}

// Both transports share the actual-send gate, after their configured holds.
func (p *process) transmit(e *pb.Envelope, send func() error) error {
	p.rateMu.Lock()
	if wait := time.Until(p.nextSend); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-p.ctx.Done():
			timer.Stop()
			p.rateMu.Unlock()
			return p.ctx.Err()
		}
	}
	if p.ctx.Err() != nil {
		p.rateMu.Unlock()
		return p.ctx.Err()
	}
	p.frameEvent(e, "send", "")
	p.nextSend = time.Now().Add(time.Duration(p.cfg.SendGapMS) * time.Millisecond)
	p.rateMu.Unlock()
	return send()
}
