"""T6: the height-1 proposer (node 0) stays silent. Round timeout fires and the chain continues."""

from harness import behaviors, check_timeout_recovered, scenario

scenario("t6a-silent-proposer", nodes=behaviors("silent_proposer", 0), checks=check_timeout_recovered)
