package main

import (
	"blockchain-lab01/src/ledger"
	runtime "blockchain-lab01/src/runtime"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"blockchain-lab01/src/realnet"
)

func main() {
	config := flag.String("config", "", "per-node JSON configuration")
	initDir := flag.String("init-dir", "", "create a fresh local cluster directory instead of starting a node")
	initOutput := flag.String("init-output-dir", "", "runtime output root written into generated node configs")
	faultTolerance := flag.Int("f", 3, "Byzantine fault tolerance; validator count is 3F+1")
	port := flag.Int("base-port", 29000, "first loopback port for init")
	duration := flag.Uint64("run-ms", 0, "run duration; zero runs until Ctrl+C")
	mempoolTTL := flag.Uint64("mempool-ttl-ms", 0, "transaction TTL written to network.json")
	accountSlots := flag.Int("account-slots", 0, "reserved transaction slots per account")
	accountQueue := flag.Int("account-queue", 0, "queued transactions allowed per account")
	globalSlots := flag.Int("global-slots", 0, "global transaction slots")
	globalQueue := flag.Int("global-queue", 0, "global queued transactions")
	inspect := flag.Bool("inspect", false, "read the on-disk ledger after stopping the node")
	exampleFaults := flag.Bool("example-network-faults", false, "generate example delay/drop/duplicate/reorder/replay policy")
	flag.Parse()
	var err error
	if *initDir != "" {
		policy := realnet.Policy{Seed: 1}
		if *exampleFaults {
			policy = realnet.Policy{Seed: 1, MinDelayMS: 2, MaxDelayMS: 30, DropBPS: 1000, DuplicateBPS: 1500, ReorderEvery: 7, ReorderHoldMS: 60, ReplayEvery: 5, ReplayAgeMS: 300, DisableAfterMS: 3000}
		}
		err = realnet.Generate(*initDir, *initOutput, *faultTolerance, *port, *duration, policy, realnet.MempoolSettings{TTLMS: *mempoolTTL, AccountSlots: *accountSlots, AccountQueue: *accountQueue, GlobalSlots: *globalSlots, GlobalQueue: *globalQueue})
	} else if *config != "" {
		var c realnet.Config
		c, err = realnet.LoadConfig(*config)
		if err == nil {
			if *inspect {
				var db *ledger.Store
				db, err = ledger.Open(filepath.Join(c.OutputDir, "ledger.db"), c.ChainID, true)
				if err == nil {
					blocks, readErr := db.ReadAll()
					closeErr := db.Close()
					err = errors.Join(readErr, closeErr)
					if err == nil {
						err = json.NewEncoder(os.Stdout).Encode(blocks)
					}
				}
			} else {
				ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
				defer cancel()
				err = realnet.Run(ctx, c)
			}
		}
	} else {
		err = fmt.Errorf("provide -config or -init-dir")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, runtime.ErrCrash) {
			os.Exit(runtime.CrashExitCode)
		}
		os.Exit(1)
	}
}
