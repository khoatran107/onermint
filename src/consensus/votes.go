package consensus

import (
	"fmt"
	"sort"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

// rounds returns every round with received evidence, sorted ascending.
func (e *Engine) rounds() []uint64 {
	seen := make(map[uint64]bool)
	for k := range e.votes {
		seen[k.round] = true
	}
	result := make([]uint64, 0, len(seen))
	for r := range seen {
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// roundChoices lists distinct vote values in validator order.
func (e *Engine) roundChoices(round uint64, phase types.VotePhase) []choice {
	seen := make(map[choice]bool)
	var result []choice
	for _, id := range e.validators {
		if v, ok := e.votes[voteKey{round, phase, id}]; ok {
			c := choice{v.IsNil, v.BlockHash}
			if !seen[c] {
				seen[c] = true
				result = append(result, c)
			}
		}
	}
	return result
}

// certificate order follows the fixed validator list, avoiding map iteration
// in outputs, proposal signatures and local evidence ordering.
func (e *Engine) certificate(round uint64, phase types.VotePhase, c choice) []types.Vote {
	var proof []types.Vote
	for _, id := range e.validators {
		v, ok := e.votes[voteKey{round, phase, id}]
		if ok && v.IsNil == c.nilVote && v.BlockHash == c.hash {
			proof = append(proof, v)
		}
	}
	return proof
}

// verifyCertificate checks a peer-supplied quorum: distinct known validators,
// each a genuine non-NIL vote for hash at this height/round/phase. label names
// the certificate in error messages ("proof" or "decision").
func (e *Engine) verifyCertificate(votes []types.Vote, round uint64, phase types.VotePhase, hash types.Hash, label string) error {
	seen := make(map[types.ValidatorID]bool)
	for _, v := range votes {
		if !e.members[v.Validator] || seen[v.Validator] {
			return fmt.Errorf("unknown or repeated %s validator", label)
		}
		if v.Height != e.height || v.Round != round || v.Phase != phase || v.IsNil || v.BlockHash != hash {
			return fmt.Errorf("%s vote scope mismatch", label)
		}
		if err := crypt.VerifyVote(e.chain, v); err != nil {
			return err
		}
		seen[v.Validator] = true
	}
	if len(seen) < e.quorum {
		return fmt.Errorf("insufficient %s quorum", label)
	}
	return nil
}
