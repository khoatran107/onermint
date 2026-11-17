package consensus

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"math"

	"blockchain-lab01/src/block"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

var (
	ErrDuplicate    = errors.New("duplicate vote")
	ErrEquivocation = errors.New("conflicting vote")
	ErrPersistence  = errors.New("ledger persistence failed")
)

type Step uint8

const (
	Propose Step = iota + 1
	Prevote
	Precommit
)

func (s Step) String() string {
	switch s {
	case Propose:
		return "PROPOSE"
	case Prevote:
		return "PREVOTE"
	case Precommit:
		return "PRECOMMIT"
	default:
		return "UNKNOWN"
	}
}

type Timer struct {
	Height, Round uint64
	Step          Step
	Delay         uint64
}
type Event struct {
	Kind, Reason  string
	Height, Round uint64
	Hash          types.Hash
	Step          Step
	Validator     types.ValidatorID
	Phase         types.VotePhase
	Proposer      int // Index of the round's proposer in the validator set; the node number.
}

// Output is a local queue of instructions, not network delivery. The owner
// must broadcast votes/proposals and schedule timers. Draining transfers copies.
type Output struct {
	Votes     []types.Vote
	Proposals []Proposal
	Timers    []Timer
	Events    []Event
	Finalized []Finalized
}
type Finalized struct {
	Block              types.Block
	OriginRound, Round uint64
	Proof              []types.Vote
}
type Equivocation struct{ First, Second types.Vote }
type Status struct {
	Height, Round uint64
	Step          Step
	Locked        bool
	LockedHash    types.Hash
	LockedRound   uint64
}

type Config struct {
	ChainID     string
	Validators  []types.ValidatorID // Fixed order determines round-robin selection.
	F           int
	PrivateKey  ed25519.PrivateKey
	BaseTimeout uint64                // Logical ticks; increases by round, saturating on overflow.
	Persist     func(Finalized) error // Production synchronously writes disk before committing.
}
type voteKey struct {
	round     uint64
	phase     types.VotePhase
	validator types.ValidatorID
}
type choice struct {
	nilVote bool
	hash    types.Hash
}
type candidate struct {
	block  types.Block
	origin uint64
	post   state.State
}

// roundValue is a block observed with a PREVOTE quorum in some round: the
// lock (rules 4-5) or the latest valid value to re-propose (with its proof).
type roundValue struct {
	set   bool
	hash  types.Hash
	round uint64
	proof []types.Vote
}

// Engine owns all mutable consensus data. Do not call it concurrently. No
// restart recovery is implemented; recreating an identity mid-run is forbidden.
type Engine struct {
	chain         string
	validators    []types.ValidatorID
	members       map[types.ValidatorID]bool
	id            types.ValidatorID
	key           ed25519.PrivateKey
	quorum        int
	faults        int
	base          uint64
	height, round uint64
	step          Step
	parent        types.Hash
	committed     state.State
	ledger        []Finalized
	persist       func(Finalized) error
	equivocations []Equivocation
	votes         map[voteKey]types.Vote
	sent          map[voteKey]types.Vote
	blocks        map[types.Hash]candidate
	proposals     map[uint64]Proposal
	locked, valid roundValue
	out           Output
}

func New(c Config) (*Engine, error) {
	if err := types.CheckValidatorCount(len(c.Validators), c.F); err != nil {
		return nil, err
	}
	if c.BaseTimeout == 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	if err := crypt.ValidateChainID(c.ChainID); err != nil {
		return nil, err
	}
	pub, err := crypt.IdentityOf(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	id := types.ValidatorID(pub)
	members := make(map[types.ValidatorID]bool)
	for _, v := range c.Validators {
		if members[v] {
			return nil, fmt.Errorf("duplicate validator")
		}
		if v == (types.ValidatorID{}) {
			return nil, fmt.Errorf("zero validator key")
		}
		members[v] = true
	}
	if !members[id] {
		return nil, fmt.Errorf("local identity not a validator")
	}
	e := &Engine{chain: c.ChainID, validators: append([]types.ValidatorID(nil), c.Validators...), members: members, id: id, key: append(ed25519.PrivateKey(nil), c.PrivateKey...), quorum: 2*c.F + 1, faults: c.F, base: c.BaseTimeout, height: 1, persist: c.Persist}
	e.resetHeight()
	e.enter(Propose)
	return e, nil
}

func (e *Engine) Proposer(height, round uint64) types.ValidatorID {
	return e.validators[e.proposerIndex(height, round)]
}

func (e *Engine) proposerIndex(height, round uint64) int {
	n := uint64(len(e.validators))
	return int(((height-1)%n + round%n) % n)
}

// blockContext is the receiver-side expectation for a block at the current
// height whose proposer was selected in originRound.
func (e *Engine) blockContext(originRound uint64) block.Context {
	return block.Context{ChainID: e.chain, Height: e.height, ParentHash: e.parent, Proposer: e.Proposer(e.height, originRound)}
}

func (e *Engine) Status() Status {
	return Status{e.height, e.round, e.step, e.locked.set, e.locked.hash, e.locked.round}
}
func (e *Engine) State() state.State { return e.committed }
func (e *Engine) Equivocations() []Equivocation {
	return append([]Equivocation(nil), e.equivocations...)
}
func (e *Engine) Ledger() []Finalized {
	result := make([]Finalized, len(e.ledger))
	for i, f := range e.ledger {
		result[i] = cloneFinalized(f)
	}
	return result
}
func (e *Engine) Drain() Output {
	out := e.out
	e.out = Output{}
	return out
}

// newEvent fills the current height/round/step context; callers may override
// fields (for example a received vote's own round) before pushing it.
func (e *Engine) newEvent(kind, reason string, h types.Hash) Event {
	return Event{Kind: kind, Reason: reason, Height: e.height, Round: e.round, Hash: h, Step: e.step, Validator: e.id, Proposer: e.proposerIndex(e.height, e.round)}
}
func (e *Engine) push(ev Event) { e.out.Events = append(e.out.Events, ev) }
func (e *Engine) event(kind, reason string, h types.Hash) {
	e.push(e.newEvent(kind, reason, h))
}
func (e *Engine) reject(err error) error { e.event("reject", err.Error(), types.Hash{}); return err }

func (e *Engine) resetHeight() {
	e.round = 0
	e.locked, e.valid = roundValue{}, roundValue{}
	e.votes = make(map[voteKey]types.Vote)
	e.sent = make(map[voteKey]types.Vote)
	e.blocks = make(map[types.Hash]candidate)
	e.proposals = make(map[uint64]Proposal)
}
func (e *Engine) enter(step Step) {
	e.step = step
	delay := uint64(math.MaxUint64)
	if e.round < math.MaxUint64 && e.base <= math.MaxUint64/(e.round+1) {
		delay = e.base * (e.round + 1)
	}
	e.out.Timers = append(e.out.Timers, Timer{e.height, e.round, step, delay})
	e.event("step", "", types.Hash{})
}

// advance enters a voting step, signs this round's vote for c, then re-runs
// the quorum checks the new vote may complete.
func (e *Engine) advance(step Step, phase types.VotePhase, c choice) error {
	e.enter(step)
	if err := e.emit(phase, c); err != nil {
		return err
	}
	return e.progress()
}

// Timeout tokens include height, round and step. Stale scheduled callbacks
// have no effect; a node can never vote at a stale height or round.
func (e *Engine) Timeout(t Timer) error {
	if t.Height != e.height || t.Round != e.round || t.Step != e.step {
		return e.reject(fmt.Errorf("stale timeout"))
	}
	e.event("timeout", "", types.Hash{})
	switch e.step {
	case Propose:
		return e.advance(Prevote, types.Prevote, choice{nilVote: true})
	case Prevote:
		return e.advance(Precommit, types.Precommit, choice{nilVote: true})
	case Precommit:
		if e.round == math.MaxUint64 {
			return e.reject(fmt.Errorf("round exhausted"))
		}
		e.round++
		e.enter(Propose)
		return e.progress()
	}
	return e.reject(fmt.Errorf("invalid timeout step"))
}

// emit signs at most one vote per (round, phase) for this validator.
func (e *Engine) emit(phase types.VotePhase, c choice) error {
	k := voteKey{e.round, phase, e.id}
	if _, ok := e.sent[k]; ok {
		return nil
	}
	if prior, ok := e.votes[k]; ok {
		// Even an externally replayed, genuine local vote must not be followed
		// by a conflicting signature. Normal callers own one Engine per key.
		if prior.IsNil != c.nilVote || prior.BlockHash != c.hash {
			return e.reject(fmt.Errorf("local signing history conflict"))
		}
		e.sent[k] = prior
		e.out.Votes = append(e.out.Votes, prior)
		return nil
	}
	v, err := crypt.SignVote(types.Vote{ChainID: e.chain, Height: e.height, Round: e.round, Phase: phase, Validator: e.id, IsNil: c.nilVote, BlockHash: c.hash}, e.key)
	if err != nil {
		return e.reject(err)
	}
	e.sent[k] = v
	e.votes[k] = v
	e.out.Votes = append(e.out.Votes, v)
	ev := e.newEvent("vote_signed", "", c.hash)
	ev.Phase = phase
	e.push(ev)
	return nil
}

// ReceiveVote accepts authenticated votes for the current height, including
// earlier rounds for late decisions and future rounds as evidence. It never
// signs in another round. Catch-up requires f+1 distinct future-round senders,
// so Byzantine validators alone cannot force an unbounded round jump.
func (e *Engine) ReceiveVote(v types.Vote) error {
	if v.Height != e.height {
		return e.reject(fmt.Errorf("unexpected vote height"))
	}
	if !e.members[v.Validator] {
		return e.reject(fmt.Errorf("unknown validator"))
	}
	k := voteKey{v.Round, v.Phase, v.Validator}
	// An exact copy includes the already verified signature and all signed
	// fields. Reject it without repeating expensive verification under a flood.
	if prior, ok := e.votes[k]; ok && prior == v {
		return e.reject(ErrDuplicate)
	}
	if err := crypt.VerifyVote(e.chain, v); err != nil {
		return e.reject(err)
	}
	if prior, ok := e.votes[k]; ok {
		if prior.IsNil == v.IsNil && prior.BlockHash == v.BlockHash {
			return e.reject(ErrDuplicate)
		}
		e.equivocations = append(e.equivocations, Equivocation{prior, v})
		return e.reject(ErrEquivocation)
	}
	e.votes[k] = v
	ev := e.newEvent("vote_received", "", v.BlockHash)
	ev.Validator, ev.Phase, ev.Round = v.Validator, v.Phase, v.Round
	e.push(ev)
	e.catchUpRound()
	return e.progress()
}

func (e *Engine) catchUpRound() {
	for _, r := range e.rounds() {
		if r <= e.round {
			continue
		}
		count := 0
		for _, id := range e.validators {
			_, pv := e.votes[voteKey{r, types.Prevote, id}]
			_, pc := e.votes[voteKey{r, types.Precommit, id}]
			if pv || pc {
				count++
			}
		}
		if count > e.faults {
			e.round = r
			e.event("round_catchup", "f+1 authenticated validators", types.Hash{})
			e.enter(Propose)
		}
	}
}

// observeQuorum records a PREVOTE quorum for a known block: it may raise the
// valid value (re-proposal candidate) and the lock (rule 4).
func (e *Engine) observeQuorum(hash types.Hash, round uint64, proof []types.Vote) {
	if _, ok := e.blocks[hash]; !ok {
		return
	}
	if !e.valid.set || round > e.valid.round {
		e.valid = roundValue{set: true, hash: hash, round: round, proof: cloneVotes(proof)}
	}
	if !e.locked.set || round > e.locked.round {
		e.locked = roundValue{set: true, hash: hash, round: round}
		e.event("lock", "PREVOTE quorum", hash)
	}
}

// progress scans received votes in validator/round order and never relies on
// map iteration to choose between outcomes. Unknown bodies cannot finalize.
func (e *Engine) progress() error {
	for _, r := range e.rounds() {
		if r > e.round {
			continue // Future rounds are evidence for catch-up only.
		}
		for _, c := range e.roundChoices(r, types.Prevote) {
			if cert := e.certificate(r, types.Prevote, c); !c.nilVote && len(cert) >= e.quorum {
				e.observeQuorum(c.hash, r, cert)
			}
		}
		for _, c := range e.roundChoices(r, types.Precommit) {
			if c.nilVote {
				continue
			}
			if cert := e.certificate(r, types.Precommit, c); len(cert) >= e.quorum {
				if cand, ok := e.blocks[c.hash]; ok {
					return e.finalize(cand, r, c.hash, cert)
				}
			}
		}
	}
	if e.step != Prevote {
		// Propose: waiting for a current-round proposal (or its timeout).
		// Precommit: waiting for a decision or the round timeout.
		return nil
	}
	for _, c := range e.roundChoices(e.round, types.Prevote) {
		if len(e.certificate(e.round, types.Prevote, c)) < e.quorum {
			continue
		}
		if !c.nilVote {
			if _, ok := e.blocks[c.hash]; !ok {
				continue
			}
		}
		return e.advance(Precommit, types.Precommit, c)
	}
	return nil
}
