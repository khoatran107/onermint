"""Generate Prometheus scrape targets from a prepared validator config directory."""

import argparse
import json
from pathlib import Path
import sys


REPO = Path(__file__).resolve().parent.parent


def main():
    if len(sys.argv) == 1:
        print("Prepare Grafana: python tests/prepare_monitoring.py --config-dir config/demo")
        print("Then start:      docker-compose -f logs/monitoring/docker-compose.yml up -d")
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path,
                        default=REPO / "logs" / "monitoring" / "runtime" / "prometheus.yml")
    args = parser.parse_args()

    configs = sorted(args.config_dir.resolve().glob("node-*.json"))
    if not configs:
        raise ValueError("configuration directory contains no node-*.json files")
    targets = []
    for path in configs:
        config = json.loads(path.read_text(encoding="utf-8"))
        address = config.get("AdminAddress", "")
        host, separator, port = address.rpartition(":")
        if not separator or not port.isdigit() or port == "0":
            raise ValueError(f"{path.name}: Grafana requires a fixed AdminAddress port")
        targets.append(f'"host.docker.internal:{int(port)}"')

    content = (
        "global:\n  scrape_interval: 2s\n"
        "scrape_configs:\n  - job_name: lab01\n    metrics_path: /metrics\n"
        f"    static_configs:\n      - targets: [{', '.join(targets)}]\n"
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(content, encoding="utf-8", newline="\n")
    print(f"Wrote {len(targets)} validator targets to {args.output}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit(f"Error: {error}")
