"""T6: the height-1 proposer (node 0) stays silent. Round timeout fires and the chain continues."""

from harness import Cluster, check, finish

NAME = "t6a-silent-proposer"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = {0: {"Behavior": "silent_proposer"}}

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT)

    net.check_agreement(COUNT)
    first = net.ledger(9)[0]
    check(first["Round"] >= 1, f"height 1 could only finish after a timeout (decided in round {first['Round']})")

finish(NAME)
