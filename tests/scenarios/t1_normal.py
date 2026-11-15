"""T1: normal run, no faults. All correct nodes finalize the same chain and state hash."""

from harness import Cluster, check, finish

NAME = "t1-normal"
TX_COUNT = 300

with Cluster(NAME) as net:
    for nonce in range(1, TX_COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(TX_COUNT)

    root, _ = net.check_agreement(TX_COUNT)

finish(NAME)
