package realnet

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	cons "blockchain-lab01/src/consensus"
	"blockchain-lab01/src/ledger"
	"blockchain-lab01/src/network"
	pb "blockchain-lab01/src/proto"
	runtime "blockchain-lab01/src/runtime"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
	"google.golang.org/grpc"
)

type Result struct {
	NodeID, PID int
	StateRoot   string
	Height      uint64
}

type transfer struct {
	peer int
	ref  types.Hash
}

// received is one request for the owner loop: an authenticated frame, a
// client transaction, or a read-only snapshot query.
type received struct {
	frame *pb.Envelope
	done  chan error
	tx    *types.Transaction
	query *snapshotQuery
}
type arrival struct {
	window uint64
	count  int
}

// localNode is the owner-loop endpoint. Production uses runtime.Runtime; tests can
// run an actual faulty peer implementation without modifying honest engines.
type localNode interface {
	Start() error
	Receive(network.Packet) error
	Submit(types.Transaction) error
	StateRoot() (types.Hash, error)
}

// inspector is the optional read-only view used by the admin endpoints.
type inspector interface {
	MempoolSnapshot() []types.Transaction
	StateSnapshot() state.Snapshot
	Status() cons.Status
	AccountNonce(types.PublicKey) uint64
}

type process struct {
	pb.UnimplementedTransportServer
	cfg                       Config
	ctx                       context.Context
	key                       ed25519.PrivateKey
	ids                       []types.ValidatorID
	runtime                   localNode
	log                       *logWriter
	in                        chan received
	jobs                      chan *pb.Envelope
	queue                     tasks
	seq, packet               uint64
	queued                    atomic.Int64 // Scheduled + dispatcher + per-peer/inflight messages.
	faults                    *faultLayer
	seen, accepted, requested map[transfer]bool
	blocked                   map[int]uint64
	arrivals                  map[int]arrival
	started                   bool
	rateMu                    sync.Mutex
	nextSend                  time.Time
	gossip                    *gossipNetwork
	sessionMu                 sync.Mutex
	sessions                  map[int]*peerSession
	streamWG                  sync.WaitGroup
	store                     *ledger.Store
	metricsMu                 sync.RWMutex
	eventCounts               map[string]uint64
}

func (p *process) now() uint64 { return uint64(time.Since(p.log.start) / time.Millisecond) }

// Record counts an event for /metrics and writes it to the event log.
func (p *process) Record(e network.Event) {
	p.metricsMu.Lock()
	if p.eventCounts == nil {
		p.eventCounts = make(map[string]uint64)
	}
	p.eventCounts[e.Event]++
	p.metricsMu.Unlock()
	p.log.record(e)
}

// event records an event of this node about peer (-1 for none).
func (p *process) event(peer int, e network.Event) {
	e.Node, e.Peer = p.cfg.NodeID, peer
	p.Record(e)
}

func (p *process) save() error {
	root, err := p.runtime.StateRoot()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(Result{NodeID: p.cfg.NodeID, PID: os.Getpid(), StateRoot: hex.EncodeToString(root[:]), Height: p.store.Height()}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(p.cfg.OutputDir, "result.json")
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Run owns one node. It serves until its context/deadline so peers can fetch
// decisions after this node finishes. Existing outputs cannot be resumed safely.
func Run(parent context.Context, c Config) (resultErr error) {
	return runWithNode(parent, c, func(p *process) (localNode, error) {
		return runtime.New(p.cfg.runtimeConfig(p.ids, p.key, p.store), p)
	})
}

// joinClose runs closer and keeps the first error.
func joinClose(resultErr *error, closer func() error) {
	if err := closer(); err != nil && *resultErr == nil {
		*resultErr = err
	}
}

// openOutputs takes the run-directory lock and opens the event log. The
// returned cleanup closes both and removes the lock.
func openOutputs(c Config) (*os.File, func() error, error) {
	if err := os.MkdirAll(c.OutputDir, 0700); err != nil {
		return nil, nil, err
	}
	lockPath := filepath.Join(c.OutputDir, "running.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	unlock := func() error { return errors.Join(lock.Close(), os.Remove(lockPath)) }
	logFlags := os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if c.Recover {
		logFlags = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	} else if _, err = os.Stat(filepath.Join(c.OutputDir, "result.json")); err == nil {
		return nil, nil, errors.Join(fmt.Errorf("existing ledger output: use a fresh run directory, or set Recover to replay it"), unlock())
	} else if !os.IsNotExist(err) {
		return nil, nil, errors.Join(err, unlock())
	}
	file, err := os.OpenFile(filepath.Join(c.OutputDir, "events.jsonl"), logFlags, 0600)
	if err != nil {
		return nil, nil, errors.Join(err, unlock())
	}
	// The log closes before the lock is released.
	return file, func() error { return errors.Join(file.Close(), unlock()) }, nil
}

func runWithNode(parent context.Context, c Config, makeNode func(*process) (localNode, error)) (resultErr error) {
	c.Defaults()
	if err := c.Validate(); err != nil {
		return err
	}
	ids, key, err := c.keys()
	if err != nil {
		return err
	}
	file, closeOutputs, err := openOutputs(c)
	if err != nil {
		return err
	}
	defer joinClose(&resultErr, closeOutputs)
	ctx, cancel := context.WithCancel(parent)
	if c.RunForMS > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(parent, time.Duration(c.RunForMS)*time.Millisecond)
	}
	defer cancel()
	store, err := ledger.Open(filepath.Join(c.OutputDir, "ledger.db"), c.ChainID, false)
	if err != nil {
		return err
	}
	defer joinClose(&resultErr, store.Close)
	if store.Height() != 0 && !c.Recover {
		return fmt.Errorf("ledger exists on disk; set Recover to replay it (voting state of the interrupted height is not restored), inspect it, or use a fresh run directory")
	}
	p := &process{cfg: c, ctx: ctx, key: key, ids: ids, log: &logWriter{file: file, start: time.Now(), trace: c.Trace, console: c.ConsoleLog}, in: make(chan received, c.QueueLimit), jobs: make(chan *pb.Envelope, c.QueueLimit), faults: newFaultLayer(c.Faults), seen: make(map[transfer]bool), accepted: make(map[transfer]bool), requested: make(map[transfer]bool), blocked: make(map[int]uint64), arrivals: make(map[int]arrival), sessions: make(map[int]*peerSession), store: store, eventCounts: make(map[string]uint64)}
	defer func() {
		if resultErr != nil {
			p.event(-1, network.Event{Event: "node_stop", Reason: resultErr.Error()})
		}
	}()
	if p.runtime, err = makeNode(p); err != nil {
		return err
	}
	if c.Recover {
		p.event(-1, network.Event{Event: "ledger_recovered", Height: store.Height()})
	}
	if p.gossip, err = p.newGossip(); err != nil {
		return err
	}
	defer func() {
		cancel()
		joinClose(&resultErr, p.gossip.close)
		joinClose(&resultErr, p.log.failure)
	}()
	servers, err := p.startServers()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go p.sender(&wg)
	defer func() {
		cancel()
		servers.stop()
		wg.Wait()
		p.streamWG.Wait()
		joinClose(&resultErr, p.log.failure)
	}()
	p.streamWG.Add(1)
	go func() { defer p.streamWG.Done(); p.gossip.connect() }()
	p.event(-1, network.Event{Event: "listen", Reason: fmt.Sprintf("pid=%d address=%s admin=%s", os.Getpid(), servers.grpcAddr, servers.adminAddr)})
	p.event(-1, network.Event{Event: "behavior", Reason: string(c.Behavior)})
	if delay := c.StartAtUnixMS - time.Now().UnixMilli(); c.StartAtUnixMS > 0 && delay > 0 {
		err = p.Schedule(uint64(delay), p.start)
	} else {
		err = p.start()
	}
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(c.OutputDir, "ready"), []byte(servers.grpcAddr.String()), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(c.OutputDir, "admin"), []byte(servers.adminAddr.String()), 0600); err != nil {
		return err
	}
	return p.loop(ctx, servers)
}

// start begins the experiment: enables input and starts the runtime.
func (p *process) start() error {
	p.started = true
	p.event(-1, network.Event{Event: "experiment_start"})
	if after := p.cfg.Faults.DisableAfterMS; after > 0 {
		if err := p.Schedule(after, func() error { p.event(-1, network.Event{Event: "faults_disabled"}); return nil }); err != nil {
			return err
		}
	}
	return p.runtime.Start()
}

type servers struct {
	grpc                *grpc.Server
	admin               *http.Server
	grpcAddr, adminAddr net.Addr
	grpcErr, adminErr   chan error
}

func (s *servers) stop() {
	_ = s.admin.Close()
	s.grpc.Stop()
}

// startServers listens on the peer gRPC address and the admin HTTP address.
func (p *process) startServers() (*servers, error) {
	listener, err := net.Listen("tcp", p.cfg.Peers[p.cfg.NodeID].Address)
	if err != nil {
		return nil, err
	}
	s := &servers{grpc: grpc.NewServer(grpc.MaxRecvMsgSize(maxFrameBytes), grpc.WaitForHandlers(true)), grpcAddr: listener.Addr(), grpcErr: make(chan error, 1), adminErr: make(chan error, 1)}
	pb.RegisterTransportServer(s.grpc, p)
	go func() { s.grpcErr <- s.grpc.Serve(listener) }()
	adminListener, err := net.Listen("tcp", p.cfg.AdminAddress)
	if err != nil {
		s.grpc.Stop()
		return nil, err
	}
	s.adminAddr = adminListener.Addr()
	s.admin = &http.Server{Handler: p.adminHandler(), ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { s.adminErr <- s.admin.Serve(adminListener) }()
	return s, nil
}

// loop is the single owner of the runtime: it runs due timers and handles
// frames, client transactions and queries one at a time until ctx ends.
func (p *process) loop(ctx context.Context, s *servers) error {
	for {
		if err := p.log.failure(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return p.save()
		}
		if t, ok := p.due(); ok {
			if err := t.run(); err != nil {
				return err
			}
			continue
		}
		timer := time.NewTimer(p.untilNext())
		select {
		case <-ctx.Done():
			timer.Stop()
			return p.save()
		case err := <-s.grpcErr:
			timer.Stop()
			if err != nil {
				return err
			}
			return fmt.Errorf("gRPC server unexpectedly stopped")
		case err := <-s.adminErr:
			timer.Stop()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return fmt.Errorf("admin HTTP server unexpectedly stopped")
		case r := <-p.in:
			timer.Stop()
			if err := p.handle(r); err != nil {
				return err
			}
		case <-timer.C:
		}
	}
}

// handle serves one owner-loop request. Rejections are replied to the caller;
// any other error is fatal and is returned after replying.
func (p *process) handle(r received) error {
	switch {
	case r.query != nil:
		r.query.reply <- p.answerQuery(r.query)
		return nil
	case r.tx != nil:
		err := network.Reject(fmt.Errorf("node awaiting experiment start"))
		if p.started {
			err = p.runtime.Submit(*r.tx)
		}
		r.done <- err
		return fatal(err)
	default:
		err := p.receive(r.frame)
		if err != nil {
			p.frameEvent(r.frame, "reject", err.Error())
		}
		if err = fatal(err); err != nil {
			r.done <- err
			return err
		}
		r.done <- nil
		return nil
	}
}

// fatal returns err unless it is a peer/client rejection.
func fatal(err error) error {
	var rejection *network.Rejection
	if err == nil || errors.As(err, &rejection) {
		return nil
	}
	return err
}

func (p *process) answerQuery(q *snapshotQuery) runtimeSnapshot {
	reader, ok := p.runtime.(inspector)
	if !ok {
		return runtimeSnapshot{err: fmt.Errorf("mempool inspection unavailable for this node runtime")}
	}
	s := runtimeSnapshot{transactions: reader.MempoolSnapshot(), state: reader.StateSnapshot(), status: reader.Status()}
	if q.sender != nil {
		s.accountNonce = reader.AccountNonce(*q.sender)
	}
	s.root, s.err = p.runtime.StateRoot()
	return s
}
