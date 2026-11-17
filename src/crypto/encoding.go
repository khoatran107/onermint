// Package crypto defines the version-1 canonical bytes and authentication rules.
// Protobuf bytes are never used as signing or hashing input.
package crypto

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"

	"blockchain-lab01/src/types"
)

const encodingVersion byte = 1

// Signing/hashing domains. Each payload starts with "<domain>:<chain ID>" so a
// signature valid in one context never validates in another.
const (
	DomainTx          = "TX"
	DomainHeader      = "HEADER"
	DomainVote        = "VOTE"
	DomainBody        = "BODY"
	DomainProposal    = "PROPOSAL"
	DomainTransport   = "TRANSPORT"
	DomainStateMerkle = "STATE-MERKLE"
)

// ValidateChainID is the one chain-ID rule: nonempty valid UTF-8.
func ValidateChainID(chainID string) error {
	if chainID == "" || !utf8.ValidString(chainID) {
		return fmt.Errorf("invalid chain ID")
	}
	return nil
}

// Encoder builds canonical bytes: version byte, uint32-length-prefixed
// "<domain>:<chain>" context, then fields. Integers are big-endian;
// strings/blobs carry a uint32 length; fixed arrays have no prefix. The first
// error sticks and is returned by Bytes.
type Encoder struct {
	data []byte
	err  error
}

func NewEncoder(domain, chainID string) *Encoder {
	e := &Encoder{data: []byte{encodingVersion}}
	if chainID == "" {
		e.err = fmt.Errorf("empty chain ID")
	}
	e.Str(domain + ":" + chainID)
	return e
}

func (e *Encoder) Byte(v byte)    { e.data = append(e.data, v) }
func (e *Encoder) Fixed(v []byte) { e.data = append(e.data, v...) }
func (e *Encoder) U32(v uint32)   { e.data = binary.BigEndian.AppendUint32(e.data, v) }
func (e *Encoder) U64(v uint64)   { e.data = binary.BigEndian.AppendUint64(e.data, v) }
func (e *Encoder) Bool(v bool) {
	if v {
		e.Byte(1)
	} else {
		e.Byte(0)
	}
}

// Count writes a uint32 element count.
func (e *Encoder) Count(n int) {
	if uint64(n) > math.MaxUint32 {
		e.fail(fmt.Errorf("count exceeds uint32"))
		return
	}
	e.U32(uint32(n))
}

func (e *Encoder) Blob(v []byte) {
	if uint64(len(v)) > math.MaxUint32 {
		e.fail(fmt.Errorf("field exceeds uint32 length"))
		return
	}
	e.U32(uint32(len(v)))
	e.data = append(e.data, v...)
}

func (e *Encoder) Str(v string) {
	if !utf8.ValidString(v) {
		e.fail(fmt.Errorf("string is not valid UTF-8"))
		return
	}
	e.Blob([]byte(v))
}

func (e *Encoder) fail(err error) {
	if e.err == nil {
		e.err = err
	}
}

func (e *Encoder) Bytes() ([]byte, error) { return e.data, e.err }

// CanonicalTxBytes excludes the signature.
func CanonicalTxBytes(tx types.Transaction) ([]byte, error) {
	e := NewEncoder(DomainTx, tx.ChainID)
	e.Fixed(tx.Sender[:])
	e.Str(tx.TargetKey)
	e.Blob(tx.Value)
	e.U64(tx.Nonce)
	return e.Bytes()
}

// CanonicalSignedTxBytes appends the fixed 64-byte signature to its payload.
func CanonicalSignedTxBytes(tx types.Transaction) ([]byte, error) {
	b, err := CanonicalTxBytes(tx)
	if err != nil {
		return nil, err
	}
	return append(b, tx.Signature[:]...), nil
}

func CanonicalHeaderBytes(h types.BlockHeader) ([]byte, error) {
	e := NewEncoder(DomainHeader, h.ChainID)
	e.U64(h.Height)
	e.Fixed(h.ParentHash[:])
	e.Fixed(h.BodyHash[:])
	e.Fixed(h.StateRoot[:])
	e.Fixed(h.Proposer[:])
	return e.Bytes()
}

func CanonicalVoteBytes(v types.Vote) ([]byte, error) {
	if v.Phase != types.Prevote && v.Phase != types.Precommit {
		return nil, fmt.Errorf("invalid vote phase")
	}
	if v.IsNil != (v.BlockHash == (types.Hash{})) {
		return nil, fmt.Errorf("inconsistent NIL vote and block hash")
	}
	e := NewEncoder(DomainVote, v.ChainID)
	e.U64(v.Height)
	e.U64(v.Round)
	e.Byte(byte(v.Phase))
	e.Fixed(v.Validator[:])
	e.Bool(v.IsNil)
	e.Fixed(v.BlockHash[:])
	return e.Bytes()
}

func CanonicalBodyBytes(chainID string, body types.BlockBody) ([]byte, error) {
	e := NewEncoder(DomainBody, chainID)
	if uint64(len(body.Transactions)) > math.MaxUint32 {
		return nil, fmt.Errorf("too many transactions")
	}
	e.Count(len(body.Transactions))
	for _, tx := range body.Transactions {
		if tx.ChainID != chainID {
			return nil, fmt.Errorf("body transaction chain mismatch")
		}
		payload, err := CanonicalTxBytes(tx)
		if err != nil {
			return nil, err
		}
		e.Blob(payload)
		e.Fixed(tx.Signature[:])
	}
	return e.Bytes()
}
