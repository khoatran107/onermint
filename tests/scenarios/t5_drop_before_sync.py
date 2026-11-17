"""T5: every message dropped until the network stabilizes. Safety holds; no double finalization."""

from harness import check, faults, scenario


def checks(net):
    # check_agreement already verified one chain at every height.
    check(net.count("fault_drop") > 0, "messages were dropped during the unstable period")
    check(net.count("faults_disabled") > 0, "faults were switched off and the chain then progressed")


scenario("t5-drop-before-sync", nodes=faults(5, DropBPS=10000, DisableAfterMS=8000), timeout=120, checks=checks)
