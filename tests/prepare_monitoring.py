"""Generate Prometheus scrape targets from a prepared validator config directory."""

import argparse
import json
from pathlib import Path
import sys


REPO = Path(__file__).resolve().parent.parent


def admin_address(config_path, config):
    """Prefer the address actually bound by the running node process."""
    output_dir = Path(config.get("OutputDir", ""))
    if not output_dir.is_absolute():
        output_dir = config_path.parent / output_dir
    runtime_address = output_dir.resolve() / "admin"
    if runtime_address.is_file():
        address = runtime_address.read_text(encoding="utf-8").strip()
        source = runtime_address
    else:
        address = config.get("AdminAddress", "")
        source = config_path

    _, separator, port = address.rpartition(":")
    if not separator or not port.isdigit() or port == "0":
        raise ValueError(
            f"{config_path.name}: no running HTTP address found; start the node first"
        )
    return int(port), source


def main():
    if len(sys.argv) == 1:
        print("Prepare Grafana: python tests/prepare_monitoring.py --config-dir config/template")
        print("Then start:      docker compose -f logs/monitoring/docker-compose.yml up -d")
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
    used_ports = set()
    runtime_count = 0
    for path in configs:
        config = json.loads(path.read_text(encoding="utf-8"))
        node_id = config["NodeID"]
        port, source = admin_address(path, config)
        if port in used_ports:
            raise ValueError(f"{path.name}: duplicate HTTP port {port}")
        used_ports.add(port)
        if source.name == "admin":
            runtime_count += 1
        targets.append(
            f'      - targets: ["host.docker.internal:{int(port)}"]\n'
            f'        labels: {{ node: "{node_id}" }}'
        )

    targets_yaml = "\n".join(targets)
    content = (
        "global:\n  scrape_interval: 2s\n"
        "scrape_configs:\n  - job_name: lab01\n    metrics_path: /metrics\n"
        f"    static_configs:\n{targets_yaml}\n"
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(content, encoding="utf-8", newline="\n")
    print(
        f"Wrote {len(targets)} validator targets to {args.output} "
        f"({runtime_count} from running node processes)"
    )


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit(f"Error: {error}")
