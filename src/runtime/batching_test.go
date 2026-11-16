package runtime

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"testing"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/ledger"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/types"
)

type scheduledCall struct {
	delay uint64
	call  func() error
}

type batchTestPort struct {
	scheduled []scheduledCall
}

func (*batchTestPort) Send(int, network.Message) error                    { return nil }
func (*batchTestPort) AcceptHeader(int, types.Hash, uint64, uint64) error { return nil }
func (*batchTestPort) Record(network.Event)                               {}
func (p *batchTestPort) Schedule(delay uint64, call func() error) error {
	p.scheduled = append(p.scheduled, scheduledCall{delay, call})
	return nil
}

func TestBatchEligibility(t *testing.T) {
	node := Runtime{minBlockTXs: 3}
	if node.batchEligible([]types.Transaction{{}, {}}) {
		t.Fatal("proposed before reaching MinBlockTXs")
	}
	if !node.batchEligible([]types.Transaction{{}, {}, {}}) {
		t.Fatal("did not propose after reaching MinBlockTXs")
	}
	node.batchReady = true
	if !node.batchEligible([]types.Transaction{{}}) {
		t.Fatal("did not propose after BlockMaxWaitMS")
	}
	if node.batchEligible(nil) {
		t.Fatal("attempted an empty block")
	}
}

func TestRuntimeWaitsForThresholdOrMaximumDelay(t *testing.T) {
	for _, test := range []struct {
		name        string
		submissions int
		fireWait    bool
	}{
		{name: "threshold", submissions: 3},
		{name: "maximum_wait", submissions: 1, fireWait: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, port, sender := newBatchRuntime(t)
			if err := node.Start(); err != nil {
				t.Fatal(err)
			}
			for nonce := 1; nonce <= test.submissions; nonce++ {
				tx, err := crypt.SignTransaction(types.Transaction{
					ChainID: "batch-test", Sender: types.PublicKey(sender[32:]),
					TargetKey: hex.EncodeToString(sender[32:]) + "/message",
					Value:     []byte{byte(nonce)}, Nonce: uint64(nonce),
				}, sender)
				if err != nil {
					t.Fatal(err)
				}
				if err := node.Submit(tx); err != nil {
					t.Fatal(err)
				}
				if nonce < 3 && !test.fireWait && node.attempted[slot{1, 0}] {
					t.Fatal("proposal was created before three transactions")
				}
			}
			if test.fireWait {
				var wait func() error
				for _, scheduled := range port.scheduled {
					if scheduled.delay == 777 {
						wait = scheduled.call
						break
					}
				}
				if wait == nil {
					t.Fatal("BlockMaxWait timer was not scheduled")
				}
				if err := wait(); err != nil {
					t.Fatal(err)
				}
			}
			if !node.attempted[slot{1, 0}] {
				t.Fatal("eligible batch did not create a proposal")
			}
		})
	}
}

func TestMempoolTTLEvictsPendingTransaction(t *testing.T) {
	node, port, sender := newBatchRuntime(t)
	tx, err := crypt.SignTransaction(types.Transaction{
		ChainID: "batch-test", Sender: types.PublicKey(sender[32:]),
		TargetKey: hex.EncodeToString(sender[32:]) + "/message",
		Value:     []byte("expires"), Nonce: 1,
	}, sender)
	if err != nil {
		t.Fatal(err)
	}
	if err = node.Submit(tx); err != nil {
		t.Fatal(err)
	}
	if node.pool.Len() != 1 {
		t.Fatal("transaction did not enter mempool")
	}
	var expire func() error
	for _, scheduled := range port.scheduled {
		if scheduled.delay == 1_234 {
			expire = scheduled.call
			break
		}
	}
	if expire == nil {
		t.Fatal("TTL eviction was not scheduled")
	}
	if err = expire(); err != nil {
		t.Fatal(err)
	}
	if node.pool.Len() != 0 {
		t.Fatal("expired transaction remained in mempool")
	}
}

func newBatchRuntime(t *testing.T) (*Runtime, *batchTestPort, ed25519.PrivateKey) {
	t.Helper()
	validators := make([]types.ValidatorID, 10)
	keys := make([]ed25519.PrivateKey, 10)
	for index := range validators {
		keys[index] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 1)}, ed25519.SeedSize))
		validators[index] = types.ValidatorID(keys[index][32:])
	}
	store, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"), "batch-test", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	port := &batchTestPort{}
	node, err := New(Config{
		ID: 0, ChainID: "batch-test", F: 3, Validators: validators, PrivateKey: keys[0],
		BaseTimeout: 100, RetryInterval: 50, TargetHeights: 1, BlockMaxWait: 777,
		MempoolTTL: 1_234,
		Store:      store, MempoolBytes: 1 << 20, MaxTxBytes: 1 << 16,
		AccountSlots: 16, AccountQueue: 64, GlobalSlots: 1024, GlobalQueue: 256,
		MinBlockTXs: 3, MaxBlockTXs: 10, MaxBlockBytes: 1 << 20,
	}, port)
	if err != nil {
		t.Fatal(err)
	}
	sender := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, ed25519.SeedSize))
	return node, port, sender
}
