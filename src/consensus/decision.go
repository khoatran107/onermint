package consensus

import (
	"fmt"
	"math"

	"blockchain-lab01/src/block"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

func cloneFinalized(f Finalized) Finalized {
	f.Block = f.Block.Clone()
	f.Proof = cloneVotes(f.Proof)
	return f
}

// finalize persists a decided block, commits its state and opens the next height.
func (e *Engine) finalize(c candidate, round uint64, hash types.Hash, proof []types.Vote) error {
	if e.height == math.MaxUint64 {
		return e.reject(fmt.Errorf("height exhausted"))
	}
	f := Finalized{Block: c.block.Clone(), OriginRound: c.origin, Round: round, Proof: cloneVotes(proof)}
	if e.persist != nil {
		if err := e.persist(f); err != nil {
			return fmt.Errorf("%w: %v", ErrPersistence, err)
		}
	} else {
		e.ledger = append(e.ledger, f)
	}
	e.out.Finalized = append(e.out.Finalized, cloneFinalized(f))
	e.committed, e.parent = c.post, hash
	e.event("finalize", "PRECOMMIT quorum", hash)
	e.height++
	e.resetHeight()
	e.enter(Propose)
	return nil
}

// verifyDecision fully checks a decision certificate for the current height:
// header, PRECOMMIT quorum, body, transactions and resulting state root.
func (e *Engine) verifyDecision(f Finalized) (candidate, types.Hash, error) {
	hash, err := e.validateDecisionHeader(f)
	if err != nil {
		return candidate{}, types.Hash{}, err
	}
	post, err := block.ValidateBody(e.blockContext(f.OriginRound), e.committed, f.Block)
	if err != nil {
		return candidate{}, types.Hash{}, err
	}
	return candidate{f.Block.Clone(), f.OriginRound, post}, hash, nil
}

// Restore rebuilds a fresh engine from the first upto persisted decisions
// (full sync from local disk). Every header, quorum proof, transaction
// signature and state root is re-verified exactly as ReceiveFinalized does,
// but Persist is not called because the blocks are already on disk. Voting
// state of the interrupted height is not recoverable; see the README.
func (e *Engine) Restore(upto uint64, get func(height uint64) (Finalized, error)) error {
	if e.height != 1 || e.persist == nil {
		return fmt.Errorf("restore requires a fresh engine with a persistent ledger")
	}
	for h := uint64(1); h <= upto; h++ {
		f, err := get(h)
		if err != nil {
			return fmt.Errorf("restore height %d: %w", h, err)
		}
		c, hash, err := e.verifyDecision(f)
		if err != nil {
			return fmt.Errorf("restore height %d: %w", h, err)
		}
		e.committed, e.parent = c.post, hash
		e.height++
	}
	e.resetHeight()
	e.out = Output{}
	e.enter(Propose)
	return nil
}

// ReceiveFinalized lets a lagging node validate a decision certificate for its
// next height without voting in the decision's round. Every block and unique
// PRECOMMIT is independently checked; a peer's finalized flag is never trusted.
// Missing earlier heights must be delivered sequentially.
func (e *Engine) ReceiveFinalized(f Finalized) error {
	c, hash, err := e.verifyDecision(f)
	if err != nil {
		return e.reject(err)
	}
	return e.finalize(c, f.Round, hash, f.Proof)
}

// ValidateDecisionHeader checks the signed header and complete finality proof
// before a receiver requests its body. It does not finalize or change state.
func (e *Engine) ValidateDecisionHeader(f Finalized) error {
	_, err := e.validateDecisionHeader(f)
	return err
}

func (e *Engine) validateDecisionHeader(f Finalized) (types.Hash, error) {
	if f.Block.Header.Height != e.height || f.OriginRound > f.Round {
		return types.Hash{}, fmt.Errorf("unexpected decision height/round")
	}
	if err := block.ValidateHeader(e.blockContext(f.OriginRound), f.Block.Header); err != nil {
		return types.Hash{}, err
	}
	hash, err := crypt.BlockHash(f.Block.Header)
	if err != nil {
		return types.Hash{}, err
	}
	if err = e.verifyCertificate(f.Proof, f.Round, types.Precommit, hash, "decision"); err != nil {
		return types.Hash{}, err
	}
	return hash, nil
}
