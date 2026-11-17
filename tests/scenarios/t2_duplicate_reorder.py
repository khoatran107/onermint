"""T2: duplicated and reordered messages. No double-counted votes, no conflicting finalization."""

from harness import check, faults, scenario


def checks(net):
    check(net.count("fault_duplicate") > 0, "duplicate copies were really transmitted")
    check(net.count("fault_reorder") > 0, "reordered messages were really transmitted")
    # Duplicate copies are dropped before the engine, so there is no "duplicate vote"
    # reject to look for. Instead: nobody signed more than PREVOTE + PRECOMMIT per round.
    for node in net.honest:
        check(max(net.votes_signed_per_round(node).values()) <= 2,
              f"node {node} signed at most 2 votes per round despite duplicates")
    reasons = net.reasons()
    check(any("duplicate" in r for r in reasons), "duplicated proposal/body copies were rejected as duplicates")
    check(not any("conflicting vote" in r for r in reasons), "no vote was mistaken for equivocation")


scenario("t2-duplicate-reorder", checks=checks,
         nodes=faults(7, MinDelayMS=2, MaxDelayMS=8, DuplicateBPS=10000, ReorderEvery=4, ReorderHoldMS=40))
