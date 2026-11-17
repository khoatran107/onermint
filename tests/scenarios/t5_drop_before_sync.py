"""T5: before synchrony 30% of messages are dropped and the rest delayed up to 300 ms; after 20 seconds the
network stabilizes. Safety holds throughout: no height is ever finalized twice or with two different states."""

import time

from harness import Cluster, faults, check, finish

NAME = "t5-drop-before-sync"
COUNT = 30
UNSTABLE_MS = 20000
NETWORK = {"BaseTimeoutMS": 1000}
# Partial loss, not a blackout: some votes and proposals do arrive, late and to only some nodes,
# so validators really act on different views of the round before the network settles.
NODES = faults(5, DropBPS=3000, MinDelayMS=20, MaxDelayMS=300, DisableAfterMS=UNSTABLE_MS)

with Cluster(NAME, NETWORK, NODES) as net:
    # Three transactions (one block's worth) every 1.5 s, so heights have to be decided
    # while the network is still lossy instead of all at once after it has settled.
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
        if nonce % 3 == 0:
            time.sleep(1.5)
    net.wait_finalized(COUNT, timeout=120)

    net.check_agreement(COUNT)  # one chain at every height at the end.
    net.check_no_double_finalization()  # ... and at every moment on the way there.
    dropped, sent = net.count("fault_drop"), net.count("fault_normal")
    check(dropped > 0 and sent > 0, f"messages were both dropped ({dropped}) and delivered ({sent}) during the unstable period")
    delayed = sum(1 for n in net.honest for row in net.events(n)
                  if row["Event"] == "fault_normal" and row.get("Reason", "").split()[1:2] not in ([], ["0"]))
    check(delayed > 0, f"delivered messages were delayed before synchrony ({delayed} held)")
    check(net.count("faults_disabled") > 0, "faults were switched off and the chain then progressed")
    early = max(sum(1 for row in net.events(n) if row["Event"] == "state_commit" and row["At"] < UNSTABLE_MS)
                for n in net.honest)
    total = len(net.ledger(net.honest[0]))
    check(total > 1, f"several heights were decided ({total}), {early} of them while the network was still unstable")

finish(NAME)
