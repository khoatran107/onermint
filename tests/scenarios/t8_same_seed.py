"""T8: same fault seed, run twice. Same ordered effects and final state hash.

Timestamps differ between runs, so the two runs are compared by finalized
transactions and state root, not byte for byte.
"""

from harness import check, faults, finish, run

NAME = "t8-same-seed"
NODES = faults(42, MinDelayMS=2, MaxDelayMS=20, DropBPS=1000, DuplicateBPS=1500, ReorderEvery=7,
               ReorderHoldMS=40, ReplayEvery=5, ReplayAgeMS=50, DisableAfterMS=8000)


def injected(attempt):
    def checks(net):
        check(net.count("fault_drop") + net.count("fault_duplicate") + net.count("fault_reorder") > 0,
              f"run {attempt}: faults were really injected")
    return checks


runs = []
for attempt in (1, 2):  # Same config folder, keys and signed transactions both times.
    print(f"-- run {attempt}")
    runs.append(run(NAME, nodes=NODES, timeout=120, checks=injected(attempt)))

(root1, txs1), (root2, txs2) = runs
check(sorted(txs1) == sorted(txs2), "both runs finalized the same signed transactions")
check(root1 == root2, f"both runs ended with the same state root {root1[:16]}...")

finish(NAME)
