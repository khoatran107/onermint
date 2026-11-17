// Package sim runs a whole validator set in one process on a virtual clock.
//
// The real network (package realnet) is driven by wall-clock time and OS
// scheduling, so two runs can never produce identical logs. Here every source
// of nondeterminism is replaced: time is a counter, message delays, drops and
// duplicates come from one seeded generator, and events are executed one at a
// time in (virtual time, sequence) order. The validators are the unmodified
// runtime.Runtime and consensus engine used by the real nodes; only the Port
// underneath them is simulated. The same Options therefore always yield the
// same byte-for-byte event log and the same final state hash.
package sim

import (
	"bytes"
	"container/heap"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/ledger"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/runtime"
	"blockchain-lab01/src/types"
)

const chainID = "lab01-sim"

// Options fully determine a run. Zero values take the defaults of Defaults.
type Options struct {
	Seed         int64
	F            int    // Byzantine tolerance; validator count is 3F+1.
	Transactions int    // Client transactions, nonces 1..Transactions.
	SubmitGapMS  uint64 // Virtual time between two client submissions.

	// Network faults, drawn from the seeded generator for every transmission.
	MinDelayMS, MaxDelayMS uint64
	DropBPS, DuplicateBPS  int    // Basis points: 10000 = always.
	DisableAfterMS         uint64 // Faults stop at this virtual time; 0 = never.

	BaseTimeoutMS, RetryMS, BlockMaxWaitMS uint64
	MinBlockTXs                            int
	LimitMS                                uint64 // Give up at this virtual time.
}

// Defaults fills unset fields with the values the T8 scenario uses.
func (o Options) Defaults() Options {
	if o.F == 0 {
		o.F = 3
	}
	if o.Transactions == 0 {
		o.Transactions = 30
	}
	if o.SubmitGapMS == 0 {
		o.SubmitGapMS = 400
	}
	if o.MaxDelayMS == 0 {
		o.MinDelayMS, o.MaxDelayMS = 2, 60
	}
	if o.BaseTimeoutMS == 0 {
		o.BaseTimeoutMS = 1000
	}
	if o.RetryMS == 0 {
		o.RetryMS = 400
	}
	if o.BlockMaxWaitMS == 0 {
		o.BlockMaxWaitMS = 2000
	}
	if o.MinBlockTXs == 0 {
		o.MinBlockTXs = 3
	}
	if o.LimitMS == 0 {
		o.LimitMS = 300_000
	}
	return o
}

// Result is everything observable about a finished run.
type Result struct {
	Log        []byte   // One JSON event per line, in execution order.
	StateRoots []string // Final state hash of every validator, by node ID.
	Heights    []uint64 // Finalized height of every validator.
	VirtualMS  uint64   // Virtual time at which the run ended.
	Events     int      // Number of log lines.
}

type task struct {
	at, seq uint64
	run     func() error
}
type queue []task

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(x any)   { *q = append(*q, x.(task)) }
func (q *queue) Pop() any {
	old := *q
	t := old[len(old)-1]
	*q = old[:len(old)-1]
	return t
}

type world struct {
	opts    Options
	now     uint64
	seq     uint64 // Orders tasks scheduled for the same virtual millisecond.
	logSeq  uint64
	packet  uint64
	rng     *rand.Rand
	tasks   queue
	nodes   []*runtime.Runtime
	log     bytes.Buffer
	encoder *json.Encoder
	events  int
}

func (w *world) schedule(delay uint64, run func() error) error {
	if delay > math.MaxUint64-w.now {
		return fmt.Errorf("virtual timer overflow")
	}
	w.seq++
	heap.Push(&w.tasks, task{w.now + delay, w.seq, run})
	return nil
}

func (w *world) record(e network.Event) {
	w.logSeq++
	e.At, e.Seq = w.now, w.logSeq
	// Encode cannot fail for this plain struct; a failure would be a programming error.
	if err := w.encoder.Encode(e); err != nil {
		panic(err)
	}
	w.events++
}

// transmit plans one message from one validator to another through the faulty network.
func (w *world) transmit(from, to int, m network.Message) error {
	w.packet++
	id := w.packet
	faulty := w.opts.DisableAfterMS == 0 || w.now < w.opts.DisableAfterMS
	base := network.Event{Node: from, Peer: to, Type: m.Kind, Height: m.Height, Round: m.Round, Ref: m.Ref, Packet: id}
	if faulty && w.rng.Intn(10000) < w.opts.DropBPS {
		base.Event = "fault_drop"
		w.record(base)
		return nil
	}
	delay := uint64(1) // A stable network still takes one tick; nothing is delivered re-entrantly.
	if faulty {
		delay = w.opts.MinDelayMS + w.rng.Uint64()%(w.opts.MaxDelayMS-w.opts.MinDelayMS+1)
		if delay == 0 {
			delay = 1
		}
	}
	copies := 1
	if faulty && w.rng.Intn(10000) < w.opts.DuplicateBPS {
		copies = 2
		base.Event = "fault_duplicate"
		w.record(base)
	}
	for c := 0; c < copies; c++ {
		packet := network.Packet{Message: network.Message{Kind: m.Kind, Ref: m.Ref, Height: m.Height, Round: m.Round, Data: append([]byte(nil), m.Data...)}, ID: id, From: from, To: to}
		if err := w.schedule(delay+uint64(c), func() error { return w.deliver(packet) }); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) deliver(p network.Packet) error {
	w.record(network.Event{Node: p.To, Peer: p.From, Event: "deliver", Type: p.Kind, Height: p.Height, Round: p.Round, Ref: p.Ref, Packet: p.ID})
	err := w.nodes[p.To].Receive(p)
	var rejection *network.Rejection
	if errors.As(err, &rejection) {
		// An invalid or stale peer message is an expected outcome, not a failure of the run.
		w.record(network.Event{Node: p.To, Peer: p.From, Event: "reject", Type: p.Kind, Height: p.Height, Round: p.Round, Ref: p.Ref, Packet: p.ID, Reason: err.Error()})
		return nil
	}
	return err
}

// port is the simulated transport and timer of one validator.
type port struct {
	w  *world
	id int
}

func (p port) Schedule(delay uint64, run func() error) error { return p.w.schedule(delay, run) }
func (p port) Record(e network.Event)                        { p.w.record(e) }
func (p port) Send(peer int, m network.Message) error {
	if peer == network.Broadcast {
		for to := range p.w.nodes {
			if to == p.id {
				continue
			}
			if err := p.w.transmit(p.id, to, m); err != nil {
				return err
			}
		}
		return nil
	}
	if peer < 0 || peer >= len(p.w.nodes) || peer == p.id {
		return fmt.Errorf("invalid simulated peer %d", peer)
	}
	return p.w.transmit(p.id, peer, m)
}
func (p port) AcceptHeader(peer int, ref types.Hash, height, round uint64) error {
	p.w.record(network.Event{Node: p.id, Peer: peer, Event: "header_accept", Type: network.Header, Height: height, Round: round, Ref: ref})
	return p.Send(peer, network.Message{Kind: network.Request, Ref: ref, Height: height, Round: round})
}

// Run executes one complete simulation. dir is scratch space for the validators'
// ledgers; it is created if missing and must not hold an earlier run.
func Run(o Options, dir string) (Result, error) {
	o = o.Defaults()
	n := 3*o.F + 1
	via := n - 1 // Client transactions enter at the last validator, like the scenarios' "via 9".
	if o.F < 1 || o.MinDelayMS > o.MaxDelayMS || o.DropBPS < 0 || o.DropBPS > 10000 || o.DuplicateBPS < 0 || o.DuplicateBPS > 10000 {
		return Result{}, fmt.Errorf("invalid simulation options")
	}
	if o.DropBPS > 0 && o.DisableAfterMS == 0 {
		return Result{}, fmt.Errorf("message loss needs DisableAfterMS, otherwise the run may never finish")
	}
	w := &world{opts: o, rng: rand.New(rand.NewSource(o.Seed))}
	w.encoder = json.NewEncoder(&w.log)

	// Identities are derived from fixed seeds, so signatures are identical in every run.
	keys := make([]ed25519.PrivateKey, n)
	validators := make([]types.ValidatorID, n)
	for i := range keys {
		keys[i] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(i + 1)}, ed25519.SeedSize))
		validators[i] = types.ValidatorID(keys[i][ed25519.SeedSize:])
	}
	client := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC1}, ed25519.SeedSize))
	clientPub := types.PublicKey(client[ed25519.SeedSize:])

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	var stores []*ledger.Store
	defer func() {
		for _, s := range stores {
			_ = s.Close()
		}
	}()
	for i := 0; i < n; i++ {
		store, err := ledger.Open(filepath.Join(dir, fmt.Sprintf("node-%02d", i)), chainID, false)
		if err != nil {
			return Result{}, err
		}
		stores = append(stores, store)
		node, err := runtime.New(runtime.Config{
			ID: i, ChainID: chainID, F: o.F, Validators: validators, PrivateKey: keys[i],
			BaseTimeout: o.BaseTimeoutMS, RetryInterval: o.RetryMS, TargetHeights: math.MaxUint64, BlockMaxWait: o.BlockMaxWaitMS,
			MempoolTTL: 600_000, Store: store, MempoolBytes: 8 << 20, MaxTxBytes: 64 << 10,
			AccountSlots: 16, AccountQueue: 1024, GlobalSlots: 1024, GlobalQueue: 4096,
			MinBlockTXs: o.MinBlockTXs, MaxBlockTXs: 128, MaxBlockBytes: (4 << 20) - (16 << 10),
		}, port{w, i})
		if err != nil {
			return Result{}, err
		}
		w.nodes = append(w.nodes, node)
	}
	for i, node := range w.nodes {
		w.record(network.Event{Node: i, Peer: -1, Event: "start"})
		if err := node.Start(); err != nil {
			return Result{}, err
		}
	}
	for nonce := 1; nonce <= o.Transactions; nonce++ {
		tx, err := crypt.SignTransaction(types.Transaction{
			ChainID: chainID, Sender: clientPub, TargetKey: hex.EncodeToString(clientPub[:]) + "/spam",
			Value: []byte(fmt.Sprintf("value-%d", nonce)), Nonce: uint64(nonce),
		}, client)
		if err != nil {
			return Result{}, err
		}
		if err = w.schedule(uint64(nonce)*o.SubmitGapMS, func() error {
			w.record(network.Event{Node: via, Peer: -1, Event: "client_submit", Reason: fmt.Sprintf("nonce=%d", tx.Nonce)})
			err := w.nodes[via].Submit(tx)
			var rejection *network.Rejection
			if errors.As(err, &rejection) {
				w.record(network.Event{Node: via, Peer: -1, Event: "client_reject", Reason: err.Error()})
				return nil
			}
			return err
		}); err != nil {
			return Result{}, err
		}
	}

	done := func() bool {
		for _, node := range w.nodes {
			if node.AccountNonce(clientPub) != uint64(o.Transactions) {
				return false
			}
		}
		return true
	}
	for !done() {
		if w.tasks.Len() == 0 {
			return Result{}, fmt.Errorf("simulation stalled at %d ms with nothing scheduled", w.now)
		}
		t := heap.Pop(&w.tasks).(task)
		if t.at > o.LimitMS {
			return Result{}, fmt.Errorf("simulation did not finish within %d virtual ms", o.LimitMS)
		}
		w.now = t.at
		if err := t.run(); err != nil {
			return Result{}, fmt.Errorf("at %d ms: %w", w.now, err)
		}
	}

	result := Result{VirtualMS: w.now}
	for i, node := range w.nodes {
		root, err := node.StateRoot()
		if err != nil {
			return Result{}, err
		}
		result.StateRoots = append(result.StateRoots, hex.EncodeToString(root[:]))
		result.Heights = append(result.Heights, stores[i].Height())
		w.record(network.Event{Node: i, Peer: -1, Event: "final_state", Height: stores[i].Height(), StateRoot: hex.EncodeToString(root[:])})
	}
	result.Log, result.Events = append([]byte(nil), w.log.Bytes()...), w.events
	return result, nil
}
