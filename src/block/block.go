// Package block constructs and validates candidate blocks. It never finalizes
// them or mutates a ledger: those are consensus responsibilities.
package block

import (
	"crypto/ed25519"
	"fmt"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

// Context must come from the receiver's local chain and proposer-selection
// rules, never from an untrusted incoming header. First height is 1 with a
// zero parent hash; later heights require a nonzero finalized parent hash.
type Context struct {
	ChainID    string
	Height     uint64
	ParentHash types.Hash
	Proposer   types.ValidatorID
}

func (c Context) validate() error {
	if err := crypt.ValidateChainID(c.ChainID); err != nil {
		return err
	}
	if c.Height == 0 {
		return fmt.Errorf("height must start at 1")
	}
	if (c.Height == 1) != (c.ParentHash == (types.Hash{})) {
		return fmt.Errorf("invalid expected parent for height")
	}
	return nil
}

// ValidateHeader checks everything available before a body arrives. A valid
// header alone does not prove body validity or finality.
func ValidateHeader(c Context, h types.BlockHeader) error {
	if err := c.validate(); err != nil {
		return err
	}
	if h.Height != c.Height {
		return fmt.Errorf("unexpected height")
	}
	if h.ParentHash != c.ParentHash {
		return fmt.Errorf("unexpected parent hash")
	}
	if h.Proposer != c.Proposer {
		return fmt.Errorf("unexpected proposer")
	}
	if err := crypt.VerifyHeader(c.ChainID, h); err != nil {
		return fmt.Errorf("header authentication: %w", err)
	}
	return nil
}

// Build signs a candidate over a copied body and a computed post-state root.
// The input state and caller-owned transaction values remain unchanged.
func Build(c Context, initial state.State, txs []types.Transaction, key ed25519.PrivateKey) (types.Block, error) {
	if err := c.validate(); err != nil {
		return types.Block{}, err
	}
	body := types.BlockBody{Transactions: txs}.Clone()
	next, err := state.Execute(c.ChainID, initial, body.Transactions)
	if err != nil {
		return types.Block{}, err
	}
	bodyHash, err := crypt.BodyHash(c.ChainID, body)
	if err != nil {
		return types.Block{}, err
	}
	root, err := next.Root(c.ChainID)
	if err != nil {
		return types.Block{}, err
	}
	header, err := crypt.SignHeader(types.BlockHeader{ChainID: c.ChainID, Height: c.Height, ParentHash: c.ParentHash, BodyHash: bodyHash, StateRoot: root, Proposer: c.Proposer}, key)
	if err != nil {
		return types.Block{}, err
	}
	return types.Block{Header: header, Body: body}, nil
}

// Validate returns an independent candidate state on success; on any failure
// it returns the original state. Callers must commit only after finalization.
func Validate(c Context, initial state.State, b types.Block) (state.State, error) {
	if err := ValidateHeader(c, b.Header); err != nil {
		return initial, err
	}
	return ValidateBody(c, initial, b)
}

// ValidateBody is Validate for a header that the caller has already checked
// with ValidateHeader under the same Context: it verifies the body commitment,
// executes the transactions and compares the resulting state root.
func ValidateBody(c Context, initial state.State, b types.Block) (state.State, error) {
	bodyHash, err := crypt.BodyHash(c.ChainID, b.Body)
	if err != nil {
		return initial, err
	}
	if bodyHash != b.Header.BodyHash {
		return initial, fmt.Errorf("body hash mismatch")
	}
	next, err := state.Execute(c.ChainID, initial, b.Body.Transactions)
	if err != nil {
		return initial, err
	}
	root, err := next.Root(c.ChainID)
	if err != nil {
		return initial, err
	}
	if root != b.Header.StateRoot {
		return initial, fmt.Errorf("state root mismatch")
	}
	return next, nil
}
