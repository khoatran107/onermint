"""Spam signed transactions into one running validator, one process call per transaction."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time

REPO = Path(__file__).resolve().parent.parent


def main():
    if len(sys.argv) == 1:
        print("Spam TX: python tests/spam_transactions.py --config-dir config/t1-normal --via 9 --count 50")
        print("Signs with an external client key (config-dir/client-key.hex, created on first use).")
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config-dir", type=Path, required=True)
    parser.add_argument("--via", type=int, default=9, help="validator that receives every transaction")
    parser.add_argument("--client-key", type=Path, help="external key file; default <config-dir>/client-key.hex")
    parser.add_argument("--count", type=int, default=50)
    parser.add_argument("--start-nonce", type=int, default=1)
    parser.add_argument("--delay", type=float, default=0.05, help="seconds between transactions")
    parser.add_argument("--record", default="spam")
    parser.add_argument("--retries", type=int, default=5, help="attempts per transaction")
    args = parser.parse_args()

    directory = args.config_dir.resolve()
    client_key = args.client_key or directory / "client-key.hex"
    config = directory / f"node-{args.via:02d}.json"
    binary = REPO / "local-runs" / ("node.exe" if os.name == "nt" else "node")
    if not config.is_file() or not binary.is_file():
        raise ValueError("missing sender config or local-runs/node; start the cluster once first")
    via_config = json.loads((directory / f"node-{args.via:02d}.json").read_text(encoding="utf-8"))
    if via_config.get("Behavior", "honest") != "honest":
        raise ValueError("choose an honest ingress node with --via")

    admitted = 0
    for i in range(args.count):  # Deterministic values: reruns produce identical signed transactions.
        nonce = args.start_nonce + i
        command = [str(binary), "-config", str(config), "-submit", f"spam-{nonce}",
                   "-nonce", str(nonce), "-record", args.record, "-via", str(args.via),
                   "-client-key", str(client_key)]
        for attempt in range(args.retries):
            result = subprocess.run(command, cwd=REPO, capture_output=True, text=True)
            if result.returncode == 0:
                admitted += 1
                break
            time.sleep(0.3)
        else:
            print(f"nonce {nonce} rejected: {result.stderr.strip()}", file=sys.stderr)
            break  # A nonce gap would make every later transaction fail too.
        time.sleep(args.delay)
    print(f"admitted {admitted}/{args.count} transactions via node {args.via}")
    if admitted != args.count:
        sys.exit(1)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
