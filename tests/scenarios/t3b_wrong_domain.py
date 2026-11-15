"""T3: node 0 sends signatures made for the wrong domain. Messages must be rejected and the reason logged."""

from harness import Cluster, check, finish

NAME = "t3b-wrong-domain"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = {0: {"Behavior": "wrong_domain"}}

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT)

    net.check_agreement(COUNT)
    check(net.count("behavior_message", nodes=[0]) > 0, "node 0 really sent faulty messages")
    reasons = net.reasons()
    check(any("signature" in r for r in reasons), f"bad signatures were rejected with a logged reason: {dict(sorted(reasons.items())[:4])}")

finish(NAME)
