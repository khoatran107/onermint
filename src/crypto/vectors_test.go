package crypto_test

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"blockchain-lab01/src/block"
	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	pb "blockchain-lab01/src/proto"
	"blockchain-lab01/src/realnet"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

// Golden vectors pin every canonical encoding byte for byte. They are shared
// with tests/test_vectors.py so the Python client cannot drift from Go.
// Regenerate only for an intentional format change: go test ./src/crypto -update
var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

const vectorsPath = "testdata/vectors.json"

func computeVectors(t *testing.T) map[string]string {
	t.Helper()
	pub, key := fixture(t)
	id := types.ValidatorID(pub)
	const chain = "lab01-vectors"
	out := map[string]string{"chain_id": chain, "seed": "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"}
	put := func(name string, b []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = hex.EncodeToString(b)
	}
	hash := func(name string, h types.Hash, err error) { t.Helper(); put(name, h[:], err) }

	tx, err := crypt.SignTransaction(types.Transaction{ChainID: chain, Sender: pub, TargetKey: hex.EncodeToString(pub[:]) + "/message", Value: []byte("hello"), Nonce: 1}, key)
	if err != nil {
		t.Fatal(err)
	}
	out["tx_target_key"], out["tx_value"], out["tx_nonce"] = tx.TargetKey, "hello", "1"
	b, err := crypt.CanonicalTxBytes(tx)
	put("tx_payload", b, err)
	put("tx_signature", tx.Signature[:], nil)
	b, err = crypt.CanonicalSignedTxBytes(tx)
	put("tx_signed", b, err)
	h, err := crypt.TransactionID(tx)
	hash("tx_id", h, err)

	tx2, err := crypt.SignTransaction(types.Transaction{ChainID: chain, Sender: pub, TargetKey: hex.EncodeToString(pub[:]) + "/other", Value: []byte{0, 1, 2}, Nonce: 2}, key)
	if err != nil {
		t.Fatal(err)
	}
	body := types.BlockBody{Transactions: []types.Transaction{tx, tx2}}
	b, err = crypt.CanonicalBodyBytes(chain, body)
	put("body", b, err)

	h, err = (state.State{}).Root(chain)
	hash("state_root_empty", h, err)
	post, err := state.Execute(chain, state.State{}, body.Transactions)
	if err != nil {
		t.Fatal(err)
	}
	h, err = post.Root(chain)
	hash("state_root_two_tx", h, err)

	blk, err := block.Build(block.Context{ChainID: chain, Height: 1, Proposer: id}, state.State{}, body.Transactions, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err = crypt.CanonicalHeaderBytes(blk.Header)
	put("header", b, err)
	put("header_signature", blk.Header.Signature[:], nil)
	blockHash, err := crypt.BlockHash(blk.Header)
	hash("block_hash", blockHash, err)

	vote, err := crypt.SignVote(types.Vote{ChainID: chain, Height: 1, Round: 2, Phase: types.Prevote, Validator: id, BlockHash: blockHash}, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err = crypt.CanonicalVoteBytes(vote)
	put("vote_prevote", b, err)
	put("vote_prevote_signature", vote.Signature[:], nil)
	nilVote, err := crypt.SignVote(types.Vote{ChainID: chain, Height: 1, Round: 3, Phase: types.Precommit, Validator: id, IsNil: true}, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err = crypt.CanonicalVoteBytes(nilVote)
	put("vote_nil_precommit", b, err)

	p, err := cons.SignProposal(cons.Proposal{ChainID: chain, Height: 1, Round: 3, OriginRound: 2, Proposer: id, Block: blk, Proof: []types.Vote{vote}}, key)
	if err != nil {
		t.Fatal(err)
	}
	b, err = cons.CanonicalProposalBytes(p)
	put("proposal", b, err)
	put("proposal_signature", p.Signature[:], nil)

	env := &pb.Envelope{ChainId: chain, Sender: 1, Recipient: ^uint32(0), Sequence: 7, Kind: "vote", Reference: blockHash[:], Height: 1, Round: 2, Payload: []byte(`{"x":1}`)}
	b, err = realnet.TransportBytes(env)
	put("transport_broadcast_vote", b, err)
	hello := &pb.Envelope{ChainId: chain, Sender: 0, Recipient: 3, Kind: "hello", Reference: make([]byte, 32), Payload: []byte{9, 9}}
	b, err = realnet.TransportBytes(hello)
	put("transport_hello", b, err)
	return out
}

func TestGoldenVectors(t *testing.T) {
	got := computeVectors(t)
	if *update {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("vector count %d, want %d", len(got), len(want))
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s changed:\n got  %s\n want %s", name, got[name], w)
		}
	}
}
