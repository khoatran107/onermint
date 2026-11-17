"""T3: node 0 sends invalid signatures. Messages must be rejected and the reason logged."""

from harness import behaviors, check_signature_rejected, scenario

scenario("t3a-invalid-signature", nodes=behaviors("invalid_signature", 0), checks=check_signature_rejected)
