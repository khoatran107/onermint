"""T8: same fault seed, run twice. Same ordered effects and final state hash.

Timestamps differ between runs, so the two runs are compared by finalized
transactions and state root, not byte for byte.
"""

from harness import Cluster, check, faults, finish

NAME = "t8-same-seed"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = faults(42, MinDelayMS=2, MaxDelayMS=20, DropBPS=1000, DuplicateBPS=1500, ReorderEvery=7,
               ReorderHoldMS=40, ReplayEvery=5, ReplayAgeMS=50, DisableAfterMS=8000)

runs = []
for attempt in (1, 2):
    print(f"-- run {attempt}")
    with Cluster(NAME, NETWORK, NODES) as net:  # Same config folder and keys both times.
        for nonce in range(1, COUNT + 1):  # Identical signed transactions each run.
            net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
        net.wait_finalized(COUNT, timeout=120)

        runs.append(net.check_agreement(COUNT))
        check(net.count("fault_drop") + net.count("fault_duplicate") + net.count("fault_reorder") > 0,
              f"run {attempt}: faults were really injected")

(root1, txs1), (root2, txs2) = runs
check(sorted(txs1) == sorted(txs2), "both runs finalized the same signed transactions")
check(root1 == root2, f"both runs ended with the same state root {root1[:16]}...")

finish(NAME)
