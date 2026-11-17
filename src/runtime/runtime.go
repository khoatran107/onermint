// Package runtime owns blockchain message handling independently of gRPC IO:
// it drives the consensus engine, the mempool and header/body dissemination.
package runtime

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/ledger"
	"blockchain-lab01/src/mempool"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

// Port isolates the node event loop from gRPC IO and real timer/log handling.
// Runtime and its Port are owned by one event loop; do not call concurrently.
type Port interface {
	Send(int, network.Message) error
	AcceptHeader(int, types.Hash, uint64, uint64) error
	Schedule(uint64, func() error) error
	Record(network.Event)
}

// BlockLimits bounds proposed blocks. A proposer waits for MinBlockTXs pending
// transactions or for BlockMaxWait, whichever comes first. Field names match
// the flat node/network JSON configuration.
type BlockLimits struct {
	MinBlockTXs, MaxBlockTXs, MaxBlockBytes int
}

type Config struct {
	ID         int
	ChainID    string
	F          int
	Validators []types.ValidatorID
	PrivateKey ed25519.PrivateKey
	// Durations are in Port.Schedule units (milliseconds for real nodes).
	BaseTimeout, RetryInterval, BlockMaxWait, MempoolTTL uint64
	TargetHeights                                        uint64
	Store                                                *ledger.Store
	Behavior                                             Behavior
	Mempool                                              mempool.Limits
	Blocks                                               BlockLimits
	Recover                                              bool // Full sync: replay and verify the local ledger before joining.
}

// advertised is a header this node announced, with the body it serves on request.
type advertised struct {
	ref           types.Hash
	header, body  []byte
	height, round uint64
	decision      bool
}
type remoteKey struct {
	peer int
	ref  types.Hash
}
type slot struct{ height, round uint64 }

// Finality certificates are much larger than current votes. Re-advertise old
// decisions less often so all-to-all catch-up advertisements do not starve the
// next height under gossip replication/duplication. Initial advertisements and
// requested bodies are still sent immediately; old decisions remain available.
const decisionRetryEvery = 4

type Runtime struct {
	port          Port
	id            int
	identity      types.ValidatorID
	engine        *cons.Engine
	chain         string
	target, retry uint64
	store         *ledger.Store
	pool          *mempool.Pool
	localTX       map[types.Hash]types.Transaction
	behavior      Behavior
	key           ed25519.PrivateKey
	blocks        struct {
		BlockLimits
		wait uint64
	}
	mempoolTTL                 uint64
	batchEpoch                 uint64
	batchScheduled, batchReady bool
	replay                     *types.Transaction
	attempted                  map[slot]bool
	ads                        []advertised
	votes                      []types.Vote
	remote                     map[remoteKey]WireHeader
	decisionRetry              uint8
}

func New(c Config, port Port) (*Runtime, error) {
	if port == nil || c.Store == nil || c.ID < 0 || c.ID >= len(c.Validators) || c.TargetHeights == 0 || c.RetryInterval == 0 || c.MempoolTTL == 0 || c.Mempool.Validate() != nil {
		return nil, fmt.Errorf("invalid node configuration")
	}
	e, err := cons.New(cons.Config{ChainID: c.ChainID, Validators: c.Validators, F: c.F, PrivateKey: c.PrivateKey, BaseTimeout: c.BaseTimeout, Persist: c.Store.Append})
	if err != nil {
		return nil, err
	}
	if types.ValidatorID(c.PrivateKey[32:]) != c.Validators[c.ID] {
		return nil, fmt.Errorf("node ID does not match private key")
	}
	if c.Recover {
		if err = e.Restore(c.Store.Height(), c.Store.Get); err != nil {
			return nil, err
		}
	}
	if c.Blocks.MinBlockTXs == 0 {
		c.Blocks.MinBlockTXs = 1 // Direct unit/runtime users retain the old immediate behavior.
	}
	if c.BlockMaxWait == 0 {
		c.BlockMaxWait = c.BaseTimeout
	}
	n := &Runtime{port: port, id: c.ID, identity: c.Validators[c.ID], engine: e, chain: c.ChainID, target: c.TargetHeights, retry: c.RetryInterval, attempted: make(map[slot]bool), remote: make(map[remoteKey]WireHeader), store: c.Store, pool: mempool.New(c.Mempool), localTX: make(map[types.Hash]types.Transaction), behavior: c.Behavior, key: append(ed25519.PrivateKey(nil), c.PrivateKey...), mempoolTTL: c.MempoolTTL}
	n.blocks.BlockLimits, n.blocks.wait = c.Blocks, c.BlockMaxWait
	return n, nil
}
func (n *Runtime) Start() error {
	if err := n.process(); err != nil {
		return err
	}
	return n.scheduleRetry()
}
func (n *Runtime) StateRoot() (types.Hash, error) { return n.engine.State().Root(n.chain) }
func (n *Runtime) Status() cons.Status            { return n.engine.Status() }
func (n *Runtime) AccountNonce(sender types.PublicKey) uint64 {
	return n.engine.State().LastNonce(sender)
}
func (n *Runtime) MempoolSnapshot() []types.Transaction { return n.pool.Snapshot() }
func (n *Runtime) StateSnapshot() state.Snapshot        { return n.engine.State().Snapshot() }

// record logs a node-local event; recordPeer logs one about a peer's message.
func (n *Runtime) record(ev network.Event) { n.recordPeer(-1, ev) }
func (n *Runtime) recordPeer(peer int, ev network.Event) {
	ev.Node, ev.Peer = n.id, peer
	n.port.Record(ev)
}

// engineFailure turns a rejected input into a logged event. Persistence
// failures are fatal and returned; everything else is a peer/protocol fault.
func (n *Runtime) engineFailure(err error, peer int, ev network.Event) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, cons.ErrPersistence) {
		return err
	}
	ev.Reason = err.Error()
	n.recordPeer(peer, ev)
	return nil
}

// ignoreQueueFull treats outbound back-pressure as already logged; the
// periodic retry will send again.
func ignoreQueueFull(err error) error {
	if errors.Is(err, network.ErrQueueFull) {
		return nil
	}
	return err
}

func (n *Runtime) send(peer int, m network.Message) error {
	return ignoreQueueFull(n.port.Send(peer, m))
}
func (n *Runtime) broadcast(m network.Message) error {
	return n.send(network.Broadcast, m)
}
func (n *Runtime) broadcastVote(v types.Vote) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return n.broadcast(network.Message{Kind: network.Vote, Height: v.Height, Round: v.Round, Data: payload})
}
func (n *Runtime) announce(a advertised) error {
	return n.broadcast(network.Message{Kind: network.Header, Ref: a.ref, Height: a.height, Round: a.round, Data: a.header})
}

// advertise announces a header. Proposal bodies are kept in RAM; finalized
// bodies are read from LevelDB on request.
func (n *Runtime) advertise(h WireHeader, body types.BlockBody) error {
	header, err := json.Marshal(h)
	if err != nil {
		return err
	}
	decision := h.Decision != nil
	var payload []byte
	if !decision {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	height, round := h.Scope()
	a := advertised{ref: crypt.HashBytes(header), header: header, body: payload, height: height, round: round, decision: decision}
	n.ads = append(n.ads, a)
	return n.announce(a)
}

// process lets this node propose if it is its turn, then publishes and
// schedules everything the engine produced.
func (n *Runtime) process() error {
	if err := n.maybePropose(); err != nil {
		return err
	}
	o := n.engine.Drain()
	for _, e := range o.Events {
		n.record(network.Event{Event: "consensus_" + e.Kind, Height: e.Height, Round: e.Round, Ref: e.Hash, Reason: e.Reason, Phase: uint8(e.Phase), Step: uint8(e.Step), Validator: hex.EncodeToString(e.Validator[:]), Proposer: &e.Proposer})
	}
	for _, timer := range o.Timers {
		if timer.Height > n.target {
			continue
		}
		if err := n.scheduleTimer(timer); err != nil {
			return err
		}
	}
	for _, p := range o.Proposals {
		if err := n.publishProposal(p); err != nil {
			return err
		}
	}
	for _, v := range o.Votes {
		if err := n.publishVote(v); err != nil {
			return err
		}
	}
	for _, f := range o.Finalized {
		if err := n.onFinalized(f); err != nil {
			return err
		}
	}
	return nil
}

func (n *Runtime) maybePropose() error {
	s := n.engine.Status()
	if s.Height > n.target || s.Step != cons.Propose || n.attempted[slot{s.Height, s.Round}] || n.engine.Proposer(s.Height, s.Round) != n.identity {
		return nil
	}
	propose, err := n.proposerTurn(s)
	if err != nil {
		return err
	}
	txs := n.selectBatch()
	if !propose || !n.batchEligible(txs) {
		return nil
	}
	n.attempted[slot{s.Height, s.Round}] = true
	_, err = n.engine.MakeProposal(txs)
	return n.engineFailure(err, -1, network.Event{Event: "proposal_failed", Height: s.Height, Round: s.Round})
}

func (n *Runtime) publishProposal(p cons.Proposal) error {
	if err := n.tamperProposal(&p); err != nil {
		return err
	}
	body := p.Block.Body
	p.Block.Body = types.BlockBody{}
	return n.advertise(WireHeader{Proposal: &p}, body)
}

func (n *Runtime) publishVote(v types.Vote) error {
	if err := n.tamperVote(&v); err != nil {
		return err
	}
	n.votes = append(n.votes, v)
	if err := n.broadcastVote(v); err != nil {
		return err
	}
	return n.afterVote(v)
}

func (n *Runtime) onFinalized(f cons.Finalized) error {
	height := f.Block.Header.Height
	n.pool.Prune(n.engine.State())
	n.resetBatchWait()
	if err := n.armBatchWait(); err != nil {
		return err
	}
	n.pruneThrough(height)
	n.record(network.Event{Event: "state_commit", Height: height, Round: f.Round, StateRoot: hex.EncodeToString(f.Block.Header.StateRoot[:])})
	body := f.Block.Body
	f.Block.Body = types.BlockBody{}
	if err := n.advertise(WireHeader{Decision: &f}, body); err != nil {
		return err
	}
	return n.afterFinalize(height)
}

// pruneThrough drops per-height state that a decision at height made obsolete.
func (n *Runtime) pruneThrough(height uint64) {
	for id := range n.localTX {
		if !n.pool.Contains(id) {
			delete(n.localTX, id)
		}
	}
	for k, h := range n.remote {
		if hh, _ := h.Scope(); hh <= height {
			delete(n.remote, k)
		}
	}
	retained := make([]advertised, 0, len(n.ads))
	for _, a := range n.ads {
		if a.decision {
			retained = append(retained, a)
		}
	}
	n.ads = retained
	pending := n.votes[:0]
	for _, v := range n.votes {
		if v.Height > height {
			pending = append(pending, v)
		}
	}
	n.votes = pending
}

func (n *Runtime) scheduleTimer(timer cons.Timer) error {
	return n.port.Schedule(timer.Delay, func() error {
		s := n.engine.Status()
		if s.Height > n.target {
			return nil
		}
		// Batch waiting is not a proposer failure. Do not change round until the
		// configured transaction threshold or maximum wait has been reached.
		if s.Height == timer.Height && s.Round == timer.Round && s.Step == cons.Propose && !s.Locked && !n.batchEligible(n.selectBatch()) {
			return n.scheduleTimer(timer)
		}
		if err := n.engine.Timeout(timer); err != nil {
			n.record(network.Event{Event: "timer_reject", Height: timer.Height, Round: timer.Round, Reason: err.Error()})
		}
		return n.process()
	})
}

// scheduleRetry periodically re-sends current-round headers and votes, old
// decisions (less often) and this node's pending client transactions.
func (n *Runtime) scheduleRetry() error {
	return n.port.Schedule(n.retry, func() error {
		if err := n.retryOnce(); err != nil {
			return err
		}
		return n.scheduleRetry()
	})
}

func (n *Runtime) retryOnce() error {
	s := n.engine.Status()
	n.decisionRetry = (n.decisionRetry + 1) % decisionRetryEvery
	for _, a := range n.ads {
		if a.decision && n.decisionRetry != 0 {
			continue
		}
		if a.decision || (a.height == s.Height && a.round == s.Round) {
			if err := n.announce(a); err != nil {
				return err
			}
		}
	}
	for _, v := range n.votes {
		if v.Height == s.Height && v.Round == s.Round {
			if err := n.broadcastVote(v); err != nil {
				return err
			}
		}
	}
	for id, tx := range n.localTX {
		if n.pool.Contains(id) {
			if err := n.gossipTX(tx); err != nil {
				return err
			}
		}
	}
	return nil
}

// Receive handles one authenticated transport packet. Invalid peer input is
// returned as network.Reject; any other error is fatal for the node.
func (n *Runtime) Receive(p network.Packet) error {
	switch p.Kind {
	case network.Transaction:
		return n.onTransaction(p)
	case network.Header:
		return n.onHeader(p)
	case network.Request:
		return n.onRequest(p)
	case network.Body:
		return n.onBody(p)
	case network.Vote:
		return n.onVote(p)
	}
	return network.Reject(fmt.Errorf("unknown message kind"))
}

func (n *Runtime) onTransaction(p network.Packet) error {
	var tx types.Transaction
	if err := json.Unmarshal(p.Data, &tx); err != nil {
		return network.Reject(err)
	}
	if err := n.admit(tx); err != nil {
		return network.Reject(err)
	}
	if err := n.duplicateGossip(tx, 2); err != nil {
		return err
	}
	return n.process()
}

func (n *Runtime) onHeader(p network.Packet) error {
	var h WireHeader
	if err := json.Unmarshal(p.Data, &h); err != nil {
		return network.Reject(err)
	}
	if err := h.check(); err != nil {
		return network.Reject(err)
	}
	if err := h.validate(n.engine, p.Height, p.Round); err != nil {
		return network.Reject(err)
	}
	n.remote[remoteKey{p.From, p.Ref}] = h
	return ignoreQueueFull(n.port.AcceptHeader(p.From, p.Ref, p.Height, p.Round))
}

func (n *Runtime) onRequest(p network.Packet) error {
	for _, a := range n.ads {
		if a.ref != p.Ref {
			continue
		}
		if a.decision {
			f, err := n.store.Get(a.height)
			if err != nil {
				return err
			}
			if a.body, err = json.Marshal(f.Block.Body); err != nil {
				return err
			}
		}
		return n.send(p.From, network.Message{Kind: network.Body, Ref: p.Ref, Height: a.height, Round: a.round, Data: a.body})
	}
	return network.Reject(fmt.Errorf("requested body unavailable"))
}

func (n *Runtime) onBody(p network.Packet) error {
	h, ok := n.remote[remoteKey{p.From, p.Ref}]
	if !ok {
		return network.Reject(fmt.Errorf("body has no accepted matching header"))
	}
	var body types.BlockBody
	if err := json.Unmarshal(p.Data, &body); err != nil {
		return network.Reject(err)
	}
	header := h.Header()
	if header.Height != p.Height {
		return network.Reject(fmt.Errorf("body transport height mismatch"))
	}
	hash, err := crypt.BodyHash(n.chain, body)
	if err != nil {
		return network.Reject(err)
	}
	if hash != header.BodyHash {
		return network.Reject(fmt.Errorf("body commitment mismatch"))
	}
	err = h.deliver(n.engine, body)
	if err = n.engineFailure(err, p.From, network.Event{Event: "reject", Type: p.Kind, Height: p.Height, Round: p.Round, Ref: p.Ref}); err != nil {
		return err
	}
	return n.process()
}

func (n *Runtime) onVote(p network.Packet) error {
	var v types.Vote
	if err := json.Unmarshal(p.Data, &v); err != nil {
		return network.Reject(err)
	}
	if v.Height != p.Height || v.Round != p.Round {
		return network.Reject(fmt.Errorf("vote transport scope mismatch"))
	}
	err := n.engine.ReceiveVote(v)
	if err = n.engineFailure(err, p.From, network.Event{Event: "reject", Type: p.Kind, Height: v.Height, Round: v.Round, Ref: v.BlockHash, Phase: uint8(v.Phase), Validator: hex.EncodeToString(v.Validator[:])}); err != nil {
		return err
	}
	return n.process()
}
