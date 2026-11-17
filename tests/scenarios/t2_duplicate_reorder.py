"""T2: duplicated and reordered messages. No double-counted votes, no conflicting finalization."""

from harness import Cluster, faults, check, finish

NAME = "t2-duplicate-reorder"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = faults(7, MinDelayMS=2, MaxDelayMS=8, DuplicateBPS=10000, ReorderEvery=4, ReorderHoldMS=40)

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT)

    net.check_agreement(COUNT)
    check(net.count("fault_duplicate") > 0, "duplicate copies were really transmitted")
    check(net.count("fault_reorder") > 0, "reordered messages were really transmitted")
    # Duplicate copies are dropped before the engine, so there is no "duplicate vote"
    # reject to look for. Instead: nobody signed more than PREVOTE + PRECOMMIT per round.
    for node in net.honest:
        signed = {}
        for row in net.events(node):
            if row["Event"] == "consensus_vote_signed":
                key = (row["Height"], row.get("Round", 0))
                signed[key] = signed.get(key, 0) + 1
        check(max(signed.values()) <= 2, f"node {node} signed at most 2 votes per round despite duplicates")
    net.check_quorum_proofs()  # Counted votes: distinct validators only, never the same vote twice.
    check(any("duplicate" in r for r in net.reasons()), "duplicated proposal/body copies were rejected as duplicates")
    check(not any("conflicting vote" in r for r in net.reasons()), "no vote was mistaken for equivocation")

finish(NAME)
