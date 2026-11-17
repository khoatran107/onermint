package runtime

import (
	"crypto/ed25519"
	"errors"

	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/types"
)

// Behavior selects an optional Byzantine fault for end-to-end tests. All
// faulty behaviour lives in this file; the honest runtime only calls the
// hooks below, which are no-ops for Honest.
type Behavior string

const (
	Honest               Behavior = "honest"
	InvalidSignature     Behavior = "invalid_signature"     // T3: flip a signature byte.
	WrongDomain          Behavior = "wrong_domain"          // T3: sign under a corrupted domain context.
	DuplicateTransaction Behavior = "duplicate_transaction" // T4: gossip every transaction again.
	ReplayTransaction    Behavior = "replay_transaction"    // T4: re-gossip an old transaction after each block.
	SilentProposer       Behavior = "silent_proposer"       // T6: never propose.
	CrashedProposer      Behavior = "crashed_proposer"      // T6: exit when it is this node's turn.
	Equivocate           Behavior = "equivocate"            // T7: double-sign every vote.
)

// Valid reports whether b is a known behaviour; empty means Honest.
func (b Behavior) Valid() bool {
	switch b {
	case "", Honest, InvalidSignature, WrongDomain, DuplicateTransaction, ReplayTransaction, SilentProposer, CrashedProposer, Equivocate:
		return true
	}
	return false
}

// ErrCrash stops a CrashedProposer node; the process exits with CrashExitCode.
var ErrCrash = errors.New("configured proposer crash")

// CrashExitCode is the expected exit status of a CrashedProposer process.
const CrashExitCode = 86

// proposerTurn runs when this node may propose; it reports whether to do so.
func (n *Runtime) proposerTurn(s cons.Status) (bool, error) {
	switch n.behavior {
	case CrashedProposer:
		n.record(network.Event{Event: "proposer_crash", Height: s.Height, Round: s.Round})
		return false, ErrCrash
	case SilentProposer:
		return false, nil
	}
	return true, nil
}

// corruptSignature damages an outgoing signature for the T3 behaviours.
// canonical returns the signed payload, needed to re-sign under a wrong domain.
func (n *Runtime) corruptSignature(canonical func() ([]byte, error), sig *types.Signature) (bool, error) {
	switch n.behavior {
	case InvalidSignature:
		sig[0] ^= 1
		return true, nil
	case WrongDomain:
		raw, err := canonical()
		if err != nil {
			return false, err
		}
		raw[5] ^= 1 // A byte of the "<DOMAIN>:<chain>" context.
		*sig = types.Signature(ed25519.Sign(n.key, raw))
		return true, nil
	}
	return false, nil
}

func (n *Runtime) tamperProposal(p *cons.Proposal) error {
	_, err := n.corruptSignature(func() ([]byte, error) { return cons.CanonicalProposalBytes(*p) }, &p.Signature)
	return err
}

func (n *Runtime) tamperVote(v *types.Vote) error {
	changed, err := n.corruptSignature(func() ([]byte, error) { return crypt.CanonicalVoteBytes(*v) }, &v.Signature)
	if changed {
		n.record(network.Event{Event: "behavior_message", Height: v.Height, Round: v.Round, Phase: uint8(v.Phase), Reason: string(n.behavior)})
	}
	return err
}

// afterVote lets an Equivocate node sign and broadcast a second vote for the
// same height, round and phase that conflicts with v (nil <-> block), so
// peers observe real double-signing. It never re-signs honestly.
func (n *Runtime) afterVote(v types.Vote) error {
	if n.behavior != Equivocate {
		return nil
	}
	w := v
	if v.IsNil {
		w.IsNil, w.BlockHash = false, crypt.HashBytes([]byte("equivocation"))
	} else {
		w.IsNil, w.BlockHash = true, types.Hash{}
	}
	w, err := crypt.SignVote(w, n.key)
	if err != nil {
		return err
	}
	n.record(network.Event{Event: "behavior_message", Height: w.Height, Round: w.Round, Phase: uint8(w.Phase), Reason: "equivocate"})
	return n.broadcastVote(w)
}

// afterAdmit remembers the first admitted transaction for ReplayTransaction.
func (n *Runtime) afterAdmit(tx types.Transaction) {
	if n.behavior == ReplayTransaction && n.replay == nil {
		c := tx.Clone()
		n.replay = &c
	}
}

// afterFinalize re-gossips the remembered (now committed) transaction.
func (n *Runtime) afterFinalize(height uint64) error {
	if n.behavior != ReplayTransaction || n.replay == nil {
		return nil
	}
	if err := n.gossipTX(*n.replay); err != nil {
		return err
	}
	n.record(network.Event{Event: "transaction_replay", Height: height})
	return nil
}

// duplicateGossip sends extra copies of tx for DuplicateTransaction.
func (n *Runtime) duplicateGossip(tx types.Transaction, copies int) error {
	if n.behavior != DuplicateTransaction {
		return nil
	}
	for i := 0; i < copies; i++ {
		if err := n.gossipTX(tx); err != nil {
			return err
		}
	}
	return nil
}
