package sim

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The fault mix of the T8 scenario: delay, loss, duplication, then a stable network.
func faulty(seed int64) Options {
	return Options{Seed: seed, Transactions: 30, MinDelayMS: 2, MaxDelayMS: 60, DropBPS: 1000, DuplicateBPS: 1500, DisableAfterMS: 8000}
}

func run(t *testing.T, o Options) Result {
	t.Helper()
	r, err := Run(o, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, root := range r.StateRoots {
		if root != r.StateRoots[0] || r.Heights[i] != r.Heights[0] {
			t.Fatalf("validators disagree: node %d root=%s height=%d, node 0 root=%s height=%d", i, root, r.Heights[i], r.StateRoots[0], r.Heights[0])
		}
	}
	if r.Heights[0] == 0 || len(r.Log) == 0 {
		t.Fatal("nothing was finalized or logged")
	}
	return r
}

// T8: the same seed run twice gives byte-identical logs and the same final state hash.
func TestSameSeedGivesByteIdenticalLogsAndState(t *testing.T) {
	first := run(t, faulty(42))
	for attempt := 2; attempt <= 4; attempt++ {
		again := run(t, faulty(42))
		if !bytes.Equal(first.Log, again.Log) {
			a, b := sha256.Sum256(first.Log), sha256.Sum256(again.Log)
			t.Fatalf("run %d log differs from run 1: %d vs %d bytes, sha256 %s vs %s", attempt, len(again.Log), len(first.Log), hex.EncodeToString(b[:8]), hex.EncodeToString(a[:8]))
		}
		if again.StateRoots[0] != first.StateRoots[0] || again.VirtualMS != first.VirtualMS {
			t.Fatalf("run %d ended differently: root %s at %d ms, run 1 root %s at %d ms", attempt, again.StateRoots[0], again.VirtualMS, first.StateRoots[0], first.VirtualMS)
		}
	}
	if !bytes.Contains(first.Log, []byte(`"fault_drop"`)) || !bytes.Contains(first.Log, []byte(`"fault_duplicate"`)) {
		t.Fatal("the run was meant to include dropped and duplicated messages")
	}
	t.Logf("seed 42: %d events, %d bytes, height %d, state root %s", first.Events, len(first.Log), first.Heights[0], first.StateRoots[0])
}

// The seed really drives the run: another seed takes a different path, yet the
// same transactions still lead every validator to the same final state.
func TestDifferentSeedChangesTheLogNotTheState(t *testing.T) {
	a, b := run(t, faulty(42)), run(t, faulty(43))
	if bytes.Equal(a.Log, b.Log) {
		t.Fatal("two different seeds produced the same log; the seed is not used")
	}
	if a.StateRoots[0] != b.StateRoots[0] {
		t.Fatalf("same transactions, different final state: %s vs %s", a.StateRoots[0], b.StateRoots[0])
	}
}

func TestMessageLossWithoutEndIsRefused(t *testing.T) {
	if _, err := Run(Options{Seed: 1, DropBPS: 5000}, t.TempDir()); err == nil {
		t.Fatal("accepted permanent message loss, which may never terminate")
	}
}
