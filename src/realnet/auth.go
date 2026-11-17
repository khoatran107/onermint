package realnet

import (
	"crypto/ed25519"
	"fmt"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/network"
	pb "blockchain-lab01/src/proto"
	"blockchain-lab01/src/types"
)

// broadcastRecipient marks an envelope published to the GossipSub topic.
const broadcastRecipient = ^uint32(0)

// TransportBytes authenticates routing/control as well as payload
// (crypto.Encoder, domain TRANSPORT:<chain>): uint32 sender/recipient, uint64
// sequence, length-prefixed kind, fixed 32-byte reference, uint64
// height/round, length-prefixed payload. The signature is excluded.
func TransportBytes(e *pb.Envelope) ([]byte, error) {
	if e == nil || crypt.ValidateChainID(e.ChainId) != nil || len(e.Reference) != 32 {
		return nil, fmt.Errorf("invalid transport envelope")
	}
	if !network.Kind(e.Kind).Valid() {
		return nil, fmt.Errorf("invalid transport kind")
	}
	enc := crypt.NewEncoder(crypt.DomainTransport, e.ChainId)
	enc.U32(e.Sender)
	enc.U32(e.Recipient)
	enc.U64(e.Sequence)
	enc.Str(e.Kind)
	enc.Fixed(e.Reference)
	enc.U64(e.Height)
	enc.U64(e.Round)
	enc.Blob(e.Payload)
	return enc.Bytes()
}

// signEnvelope sets e.Signature with this node's transport key.
func (p *process) signEnvelope(e *pb.Envelope) error {
	b, err := TransportBytes(e)
	if err != nil {
		return err
	}
	e.Signature = ed25519.Sign(p.key, b)
	return nil
}

func VerifyEnvelope(chain string, recipient int, ids []types.ValidatorID, e *pb.Envelope) error {
	if e == nil || e.ChainId != chain || int(e.Sender) >= len(ids) {
		return fmt.Errorf("unexpected transport chain/identity")
	}
	if e.Recipient == broadcastRecipient {
		if !network.Kind(e.Kind).Broadcastable() {
			return fmt.Errorf("only headers/votes may be broadcast")
		}
	} else if e.Recipient != uint32(recipient) {
		return fmt.Errorf("unexpected transport recipient")
	}
	b, err := TransportBytes(e)
	if err != nil {
		return err
	}
	pub := ids[e.Sender]
	if !ed25519.Verify(ed25519.PublicKey(pub[:]), b, e.Signature) {
		return fmt.Errorf("invalid transport signature")
	}
	return nil
}

// refOf reads an envelope's fixed 32-byte reference.
func refOf(e *pb.Envelope) types.Hash {
	var ref types.Hash
	copy(ref[:], e.Reference)
	return ref
}

// peerOf maps an envelope recipient to a peer index (network.Broadcast for gossip).
func peerOf(recipient uint32) int {
	if recipient == broadcastRecipient {
		return network.Broadcast
	}
	return int(recipient)
}
