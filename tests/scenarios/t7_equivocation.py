"""T7: f=3 validators (nodes 0, 1, 2) double-sign every vote. No conflicting finalization."""

from harness import Cluster, check, finish

NAME = "t7-equivocation"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = {i: {"Behavior": "equivocate"} for i in (0, 1, 2)}

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT, nodes=[3, 4, 5, 6, 7, 8, 9])

    net.check_agreement(COUNT)
    check(net.count("behavior_message", nodes=[0, 1, 2]) > 0, "the three Byzantine nodes really sent conflicting votes")
    reasons = net.reasons()
    check(any("conflicting vote" in r for r in reasons), f"honest nodes detected the equivocation: {reasons}")

finish(NAME)
