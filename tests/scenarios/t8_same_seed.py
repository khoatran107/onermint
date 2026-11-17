"""T8: same seed, run twice. Byte-identical logs and the same final state hash.

Part 1 is the required check. Ten validators (the same runtime and consensus code the real
nodes use) run in one process on a virtual clock; every delay, drop and duplicate comes from
the seed. Two runs with the same seed must write byte-identical event logs and end in the
same state hash.

Part 2 repeats the fault seed on the real 10-process network. Real processes are scheduled by
the OS and stamped with wall-clock time, so their logs cannot match byte for byte; there the
two runs are compared by finalized transactions and state root.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess

from harness import REPO, Cluster, check, faults, finish

NAME = "t8-same-seed"
COUNT = 30
SEED = 42
NETWORK = {"BaseTimeoutMS": 1000}
NODES = faults(SEED, MinDelayMS=2, MaxDelayMS=20, DropBPS=1000, DuplicateBPS=1500, ReorderEvery=7,
               ReorderHoldMS=40, ReplayEvery=5, ReplayAgeMS=50, DisableAfterMS=8000)

# --- Part 1: deterministic simulation, byte-identical logs --------------------------------
print("-- deterministic simulation")
go = os.environ.get("LAB01_GO", "go")
binary = REPO / "local-runs" / ("sim.exe" if os.name == "nt" else "sim")
binary.parent.mkdir(exist_ok=True)
subprocess.run([go, "build", "-o", str(binary), "./src/cmd/sim"], cwd=REPO, check=True)
logs = REPO / "local-runs" / f"{NAME}-sim"
logs.mkdir(exist_ok=True)


def simulate(seed, name):
    """One simulation in its own process; returns (its summary, the bytes of its log file)."""
    path = logs / name
    done = subprocess.run([str(binary), "-seed", str(seed), "-txs", str(COUNT), "-out", str(path)],
                          cwd=REPO, capture_output=True, text=True)
    if done.returncode != 0:
        raise RuntimeError(f"simulation with seed {seed} failed: {done.stderr.strip()}")
    return json.loads(done.stdout), path.read_bytes()


(first, log1), (second, log2) = simulate(SEED, "run1.jsonl"), simulate(SEED, "run2.jsonl")
other, log3 = simulate(SEED + 1, "other-seed.jsonl")
digest = hashlib.sha256(log1).hexdigest()
check(len(log1) > 0 and log1 == log2,
      f"same seed, two runs: logs are byte-identical ({first['events']} events, {len(log1)} bytes, sha256 {digest[:16]}...)")
check(first["state_root"] == second["state_root"] and first["height"] == second["height"] > 0,
      f"same seed, two runs: same final state hash {first['state_root'][:16]}... at height {first['height']}")
check(b'"fault_drop"' in log1 and b'"fault_duplicate"' in log1, "the compared runs include dropped and duplicated messages")
check(log3 != log1, f"a different seed ({SEED + 1}) gives a different log, so the seed really drives the run")
check(other["state_root"] == first["state_root"], "the same transactions end in the same state hash under either seed")
print(f"   logs kept in {logs}")

# --- Part 2: the same fault seed on the real network ---------------------------------------
runs = []
for attempt in (1, 2):
    print(f"-- real network, run {attempt}")
    with Cluster(NAME, NETWORK, NODES) as net:  # Same config folder and keys both times.
        for nonce in range(1, COUNT + 1):  # Identical signed transactions each run.
            net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
        net.wait_finalized(COUNT, timeout=120)

        runs.append(net.check_agreement(COUNT))
        check(net.count("fault_drop") + net.count("fault_duplicate") + net.count("fault_reorder") > 0,
              f"run {attempt}: faults were really injected")

(root1, txs1), (root2, txs2) = runs
check(txs1 == txs2, "real network: both runs finalized the same transactions in the same order")
check(root1 == root2, f"real network: both runs ended with the same state root {root1[:16]}...")

finish(NAME)
