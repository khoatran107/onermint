"""T7: f = 3 Byzantine validators double-sign every vote. No conflicting finalization."""

from harness import behaviors, check, scenario

BYZANTINE = (0, 1, 2)


def checks(net):
    check(net.count("behavior_message", nodes=list(BYZANTINE)) > 0, "the three Byzantine nodes really sent conflicting votes")
    reasons = net.reasons()
    check(any("conflicting vote" in r for r in reasons), f"honest nodes detected the equivocation: {reasons}")


scenario("t7-equivocation", nodes=behaviors("equivocate", *BYZANTINE),
         wait_nodes=[n for n in range(10) if n not in BYZANTINE], checks=checks)
