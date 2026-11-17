package runtime

import (
	"fmt"

	cons "blockchain-lab01/src/consensus"
	"blockchain-lab01/src/types"
)

// WireHeader is the JSON payload of a network.Header message: exactly one of
// a body-less proposal or a body-less finality certificate. Bodies travel
// separately, only after the receiver accepts the header.
type WireHeader struct {
	Proposal *cons.Proposal  `json:",omitempty"`
	Decision *cons.Finalized `json:",omitempty"`
}

func (h WireHeader) check() error {
	if (h.Proposal == nil) == (h.Decision == nil) {
		return fmt.Errorf("header must contain exactly one envelope")
	}
	return nil
}

// Header is the signed block header carried by either envelope.
func (h WireHeader) Header() types.BlockHeader {
	if h.Proposal != nil {
		return h.Proposal.Block.Header
	}
	return h.Decision.Block.Header
}

// Scope is the (height, round) a transport message must be labelled with.
func (h WireHeader) Scope() (height, round uint64) {
	if h.Proposal != nil {
		return h.Proposal.Height, h.Proposal.Round
	}
	return h.Decision.Block.Header.Height, h.Decision.Round
}

// validate checks transport labelling, that no body is inlined, and then the
// engine's header-only authentication.
func (h WireHeader) validate(e *cons.Engine, height, round uint64) error {
	label, emptyBody := "decision", "decision header contains body"
	if h.Proposal != nil {
		label, emptyBody = "proposal", "header contains body"
	}
	if hh, hr := h.Scope(); hh != height || hr != round {
		return fmt.Errorf("%s transport scope mismatch", label)
	}
	if h.body() > 0 {
		return fmt.Errorf("%s", emptyBody)
	}
	if h.Proposal != nil {
		return e.ValidateProposalHeader(*h.Proposal)
	}
	return e.ValidateDecisionHeader(*h.Decision)
}

func (h WireHeader) body() int {
	if h.Proposal != nil {
		return len(h.Proposal.Block.Body.Transactions)
	}
	return len(h.Decision.Block.Body.Transactions)
}

// deliver joins the accepted header with its body and hands it to the engine.
func (h WireHeader) deliver(e *cons.Engine, body types.BlockBody) error {
	if h.Proposal != nil {
		p := *h.Proposal
		p.Block.Body = body
		return e.ReceiveProposal(p)
	}
	f := *h.Decision
	f.Block.Body = body
	return e.ReceiveFinalized(f)
}
