// Package consensus implements Lab01's two-phase, round-based locking protocol.
// It is a single-threaded state machine; the real node runtime supplies messages
// and timeout events. The engine itself owns no sockets or wall-clock timers.
package consensus

import (
	"crypto/ed25519"
	"fmt"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

// Proposal separates the current proposer/round from the immutable block's
// original proposer. A reproposal requires a prior-round PREVOTE certificate.
// Proof ordering is signed exactly as transmitted, not reordered by receivers.
type Proposal struct {
	ChainID     string
	Height      uint64
	Round       uint64
	OriginRound uint64
	Proposer    types.ValidatorID
	Block       types.Block
	Proof       []types.Vote
	Signature   types.Signature
}

// CanonicalProposalBytes extends the domain scheme with PROPOSAL:<chain>.
// Format (crypto.Encoder): version=1, context, uint64 height/round/originRound,
// 32-byte proposer, block hash, original header signature, uint32 proof count,
// then each length-prefixed canonical VOTE payload and its fixed signature.
// BodyHash authenticates the body; the header signature is bound here too.
// The proposal's own signature is excluded.
func CanonicalProposalBytes(p Proposal) ([]byte, error) {
	h, err := crypt.BlockHash(p.Block.Header)
	if err != nil {
		return nil, err
	}
	if crypt.ValidateChainID(p.ChainID) != nil {
		return nil, fmt.Errorf("invalid proposal chain ID")
	}
	e := crypt.NewEncoder(crypt.DomainProposal, p.ChainID)
	e.U64(p.Height)
	e.U64(p.Round)
	e.U64(p.OriginRound)
	e.Fixed(p.Proposer[:])
	e.Fixed(h[:])
	e.Fixed(p.Block.Header.Signature[:])
	e.Count(len(p.Proof))
	for _, v := range p.Proof {
		payload, err := crypt.CanonicalVoteBytes(v)
		if err != nil {
			return nil, err
		}
		e.Blob(payload)
		e.Fixed(v.Signature[:])
	}
	return e.Bytes()
}

func SignProposal(p Proposal, key ed25519.PrivateKey) (Proposal, error) {
	b, err := CanonicalProposalBytes(p)
	if err != nil {
		return p, err
	}
	sig, err := crypt.Sign(key, types.PublicKey(p.Proposer), b)
	if err != nil {
		return p, fmt.Errorf("proposal signing: %w", err)
	}
	p.Signature = sig
	return p, nil
}

func VerifyProposal(chainID string, p Proposal) error {
	if chainID == "" || p.ChainID != chainID {
		return fmt.Errorf("unexpected proposal chain")
	}
	b, err := CanonicalProposalBytes(p)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(p.Proposer[:]), b, p.Signature[:]) {
		return fmt.Errorf("invalid proposal signature")
	}
	return nil
}

func cloneProposal(p Proposal) Proposal {
	p.Block = p.Block.Clone()
	p.Proof = cloneVotes(p.Proof)
	return p
}

func cloneVotes(v []types.Vote) []types.Vote { return append([]types.Vote(nil), v...) }
