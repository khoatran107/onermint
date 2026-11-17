"""T3: node 0 signs under a wrong domain context. Messages must be rejected and the reason logged."""

from harness import behaviors, check_signature_rejected, scenario

scenario("t3b-wrong-domain", nodes=behaviors("wrong_domain", 0), checks=check_signature_rejected)
