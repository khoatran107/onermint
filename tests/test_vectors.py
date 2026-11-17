"""Check tests/client_lib.py against the Go golden vectors (src/crypto/testdata/vectors.json).

Run: python tests/test_vectors.py   (also run by tests/run_tests.py)
"""

import json
from pathlib import Path
import sys

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

sys.path.insert(0, str(Path(__file__).resolve().parent))
from client_lib import Wallet, canonical_tx_bytes  # noqa: E402

VECTORS = Path(__file__).resolve().parent.parent / "src" / "crypto" / "testdata" / "vectors.json"


def main():
    v = json.loads(VECTORS.read_text(encoding="utf-8"))
    wallet = Wallet(Ed25519PrivateKey.from_private_bytes(bytes.fromhex(v["seed"])))
    payload = canonical_tx_bytes(v["chain_id"], wallet.public, v["tx_target_key"],
                                 v["tx_value"].encode(), int(v["tx_nonce"]))
    if payload.hex() != v["tx_payload"]:
        sys.exit("FAIL canonical_tx_bytes differs from Go crypto.CanonicalTxBytes")
    tx = wallet.sign_transaction(v["chain_id"], v["tx_target_key"].split("/", 1)[1],
                                 v["tx_value"], int(v["tx_nonce"]))
    if bytes(tx["Signature"]).hex() != v["tx_signature"]:
        sys.exit("FAIL Python transaction signature differs from Go")
    print("ok   tests/client_lib.py matches Go golden vectors")


if __name__ == "__main__":
    main()
