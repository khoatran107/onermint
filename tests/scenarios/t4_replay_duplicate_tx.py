"""T4: replayed and duplicated transactions. Each is applied at most once."""

from harness import check, scenario


def checks(net):
    # check_agreement already verified: no (sender, nonce) twice, exactly COUNT transactions.
    reasons = net.reasons()
    check(net.count("transaction_reject") + sum(v for r, v in reasons.items() if "nonce" in r) > 0,
          f"replays/duplicates were rejected: {reasons}")


scenario("t4-replay-duplicate-tx", checks=checks,
         nodes={0: {"Behavior": "replay_transaction"}, 1: {"Behavior": "duplicate_transaction"}})
