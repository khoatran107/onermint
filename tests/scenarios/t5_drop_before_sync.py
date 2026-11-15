"""T5: every message is dropped for the first 8 seconds, then the network stabilizes. Safety holds throughout."""

from harness import Cluster, faults, check, finish

NAME = "t5-drop-before-sync"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = faults(5, DropBPS=10000, DisableAfterMS=8000)

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT, timeout=120)

    net.check_agreement(COUNT)  # one chain at every height: no double finalization.
    check(net.count("fault_drop") > 0, "messages were dropped during the unstable period")
    check(net.count("faults_disabled") > 0, "faults were switched off and the chain then progressed")

finish(NAME)
