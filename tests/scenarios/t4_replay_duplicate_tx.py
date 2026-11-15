"""T4: replayed and duplicated transactions. Each transaction is applied at most once."""

from harness import Cluster, check, finish

NAME = "t4-replay-duplicate-tx"
COUNT = 30
NETWORK = {"BaseTimeoutMS": 1000}
NODES = {0: {"Behavior": "replay_transaction"}, 1: {"Behavior": "duplicate_transaction"}}

with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT)

    net.check_agreement(COUNT)  # includes: no (sender, nonce) twice, exactly COUNT transactions.
    reasons = net.reasons()
    check(net.count("transaction_reject") + sum(v for r, v in reasons.items() if "nonce" in r) > 0,
          f"replays/duplicates were rejected: {reasons}")

finish(NAME)
