"""T6: the height-1 proposer (node 0) crashes. Round timeout fires and the chain continues."""

from harness import behaviors, check_timeout_recovered, scenario

scenario("t6b-crashed-proposer", nodes=behaviors("crashed_proposer", 0), checks=check_timeout_recovered)
