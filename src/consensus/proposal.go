package consensus

import (
	"fmt"

	"blockchain-lab01/src/block"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

// MakeProposal is invoked only by the current proposer. A known quorum-backed
// candidate takes precedence over new transactions, ensuring locks can survive
// round changes. Publication is separate from local validation/prevoting.
func (e *Engine) MakeProposal(txs []types.Transaction) (Proposal, error) {
	if e.Proposer(e.height, e.round) != e.id || e.step != Propose {
		return Proposal{}, e.reject(fmt.Errorf("not current proposing validator/step"))
	}
	if _, exists := e.proposals[e.round]; exists {
		return Proposal{}, e.reject(fmt.Errorf("proposal already accepted"))
	}
	p := Proposal{ChainID: e.chain, Height: e.height, Round: e.round, OriginRound: e.round, Proposer: e.id}
	if e.valid.set {
		if e.valid.round >= e.round {
			return Proposal{}, e.reject(fmt.Errorf("current-round quorum already observed; await proposal or timeout"))
		}
		c := e.blocks[e.valid.hash]
		p.Block = c.block.Clone()
		p.OriginRound = c.origin
		p.Proof = cloneVotes(e.valid.proof)
	} else {
		b, err := block.Build(e.blockContext(e.round), e.committed, txs, e.key)
		if err != nil {
			return Proposal{}, e.reject(err)
		}
		p.Block = b
	}
	p, err := SignProposal(p, e.key)
	if err != nil {
		return Proposal{}, e.reject(err)
	}
	if err = e.ReceiveProposal(p); err != nil {
		return Proposal{}, err
	}
	e.out.Proposals = append(e.out.Proposals, cloneProposal(p))
	return cloneProposal(p), nil
}

// ReceiveProposal validates and executes a complete proposal, then prevotes
// for it unless locked on a different block (rule 5).
func (e *Engine) ReceiveProposal(p Proposal) error {
	hash, proofRound, err := e.validateProposalHeader(p)
	if err != nil {
		return e.reject(err)
	}
	if _, exists := e.proposals[e.round]; exists {
		return e.reject(fmt.Errorf("duplicate/conflicting proposal"))
	}
	post, err := block.ValidateBody(e.blockContext(p.OriginRound), e.committed, p.Block)
	if err != nil {
		return e.reject(err)
	}
	e.blocks[hash] = candidate{p.Block.Clone(), p.OriginRound, post}
	e.proposals[e.round] = cloneProposal(p)
	if len(p.Proof) > 0 {
		e.observeQuorum(hash, proofRound, p.Proof)
	}
	e.event("proposal", "validated", hash)
	if e.step == Propose {
		c := choice{hash: hash}
		if e.locked.set && e.locked.hash != hash {
			c = choice{nilVote: true}
		}
		return e.advance(Prevote, types.Prevote, c)
	}
	return e.progress()
}

// ValidateProposalHeader authenticates the envelope, header and quorum evidence
// without reading a body or changing consensus state. Header acceptance is not
// proposal acceptance: ReceiveProposal still validates and executes the body.
func (e *Engine) ValidateProposalHeader(p Proposal) error {
	_, _, err := e.validateProposalHeader(p)
	return err
}

// validateProposalHeader returns the block hash and, for a re-proposal, the
// round of its PREVOTE proof (0 without proof).
func (e *Engine) validateProposalHeader(p Proposal) (types.Hash, uint64, error) {
	if p.Height != e.height || p.Round != e.round || p.OriginRound > p.Round {
		return types.Hash{}, 0, fmt.Errorf("unexpected proposal height/round")
	}
	if p.Proposer != e.Proposer(e.height, e.round) {
		return types.Hash{}, 0, fmt.Errorf("wrong round proposer")
	}
	if err := VerifyProposal(e.chain, p); err != nil {
		return types.Hash{}, 0, err
	}
	hash, err := crypt.BlockHash(p.Block.Header)
	if err != nil {
		return types.Hash{}, 0, err
	}
	if err = block.ValidateHeader(e.blockContext(p.OriginRound), p.Block.Header); err != nil {
		return types.Hash{}, 0, err
	}
	proofRound, err := e.checkProof(p, hash)
	if err != nil {
		return types.Hash{}, 0, err
	}
	return hash, proofRound, nil
}

// checkProof validates the PREVOTE certificate that justifies a re-proposal
// of a block first proposed in an earlier round.
func (e *Engine) checkProof(p Proposal, hash types.Hash) (uint64, error) {
	if len(p.Proof) == 0 {
		if p.OriginRound != p.Round {
			return 0, fmt.Errorf("reproposal requires prior PREVOTE quorum")
		}
		return 0, nil
	}
	r := p.Proof[0].Round
	if r >= p.Round || r < p.OriginRound {
		return 0, fmt.Errorf("invalid proof round")
	}
	if err := e.verifyCertificate(p.Proof, r, types.Prevote, hash, "proof"); err != nil {
		return 0, err
	}
	return r, nil
}
