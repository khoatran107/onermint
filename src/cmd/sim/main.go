// Command sim runs the deterministic in-process simulation (package sim) once
// and writes its event log. Running it twice with the same flags must produce
// byte-identical log files and the same final state hash (test T8).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"blockchain-lab01/src/sim"
)

func main() {
	seed := flag.Int64("seed", 42, "seed for every random network decision")
	txs := flag.Int("txs", 30, "client transactions to finalize")
	out := flag.String("out", "", "file that receives the event log (required)")
	minDelay := flag.Uint64("min-delay-ms", 2, "minimum message delay while faults are on")
	maxDelay := flag.Uint64("max-delay-ms", 60, "maximum message delay while faults are on")
	drop := flag.Int("drop-bps", 1000, "message loss in basis points while faults are on")
	duplicate := flag.Int("duplicate-bps", 1500, "message duplication in basis points while faults are on")
	disable := flag.Uint64("disable-after-ms", 8000, "virtual time at which the network becomes stable")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "provide -out")
		os.Exit(2)
	}
	scratch, err := os.MkdirTemp("", "lab01-sim-")
	if err == nil {
		defer os.RemoveAll(scratch)
		var result sim.Result
		result, err = sim.Run(sim.Options{Seed: *seed, Transactions: *txs, MinDelayMS: *minDelay, MaxDelayMS: *maxDelay, DropBPS: *drop, DuplicateBPS: *duplicate, DisableAfterMS: *disable}, scratch)
		if err == nil {
			for i, root := range result.StateRoots {
				if root != result.StateRoots[0] || result.Heights[i] != result.Heights[0] {
					err = fmt.Errorf("validators disagree: node %d has root %s at height %d", i, root, result.Heights[i])
				}
			}
		}
		if err == nil {
			err = os.WriteFile(*out, result.Log, 0o644)
		}
		if err == nil {
			digest := sha256.Sum256(result.Log)
			err = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"seed": *seed, "events": result.Events, "log_bytes": len(result.Log), "log_sha256": hex.EncodeToString(digest[:]),
				"state_root": result.StateRoots[0], "height": result.Heights[0], "virtual_ms": result.VirtualMS,
			})
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(scratch)
		os.Exit(1)
	}
}
