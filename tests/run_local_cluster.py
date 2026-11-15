"""Build, configure and run real Go nodes. Python 3.9+, standard library only."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


DEFAULT_F = 3

REPO = Path(__file__).resolve().parent.parent


def handle_interrupt(_signum, _frame):
    raise KeyboardInterrupt


def build_node(go):
    output = REPO / "local-runs"
    output.mkdir(exist_ok=True)
    binary = output / ("node.exe" if os.name == "nt" else "node")
    subprocess.run([go, "build", "-buildvcs=false", "-o", str(binary), "./src/cmd/node"],
                   cwd=REPO, check=True)
    return binary


def prepare_cluster(go, name, fault_tolerance):
    if fault_tolerance < 3:
        raise ValueError("F must be at least 3 for this lab; N is always 3F + 1.")
    if not name or Path(name).name != name:
        raise ValueError("--init must be one simple scenario name, for example demo.")
    directory = (REPO / "config" / name).resolve()
    output = (REPO / "local-runs" / name).resolve()
    if directory.exists() or output.exists():
        raise ValueError("Config or output already exists; choose a fresh --init name.")
    directory.parent.mkdir(parents=True, exist_ok=True)
    relative_output = os.path.relpath(output, directory)
    command = [go, "run", "-buildvcs=false", "./src/cmd/node",
               "-init-dir", str(directory), "-f", str(fault_tolerance),
               "-init-output-dir", relative_output]
    subprocess.run(command, cwd=REPO, check=True)
    return directory


def load_configs(directory):
    paths = sorted(directory.glob("node-*.json"), key=lambda p: int(p.stem.split("-")[-1]))
    if len(paths) < 8 or (len(paths) - 1) % 3:
        raise ValueError("Configuration directory must contain a valid complete validator set.")
    configs = []
    membership = None
    for node_id, path in enumerate(paths):
        config = json.loads(path.read_text(encoding="utf-8"))
        if config.get("NetworkFile"):
            shared = Path(config["NetworkFile"])
            if not shared.is_absolute():
                shared = path.parent / shared
            config.update(json.loads(shared.read_text(encoding="utf-8")))
        config.setdefault("Behavior", "honest")
        config.setdefault("AdminAddress", "127.0.0.1:0")
        config.setdefault("OutputDir", f"node-{node_id:02d}")
        config.setdefault("RunForMS", 0)
        config.setdefault("TargetHeights", 0)
        if config["NodeID"] != node_id:
            raise ValueError(f"Unexpected node ID in {path}")
        if len(config["Peers"]) != len(paths) or config["F"] != (len(paths) - 1) // 3:
            raise ValueError(f"Configuration directory does not cover the fixed validator set in {path}")
        current = (config["ChainID"], config["F"], config["Peers"])
        if membership is not None and current != membership:
            raise ValueError(f"Different ordered validator configuration in {path}")
        membership = current
        if any(not peer.get("GossipAddress") for peer in config["Peers"]):
            raise ValueError("Missing GossipAddress; prepare fresh configurations for the new transport.")
        output = Path(config["OutputDir"])
        if not output.is_absolute():
            output = (path.parent / output).resolve()
        configs.append((path, config, output))
    if sum(c[1]["Behavior"] != "honest" for c in configs) > configs[0][1]["F"]:
        raise ValueError("Configured dishonest node count exceeds f; safety assumptions would not hold.")
    fixed_admin = [c[1]["AdminAddress"] for c in configs if not c[1]["AdminAddress"].endswith(":0")]
    if len(fixed_admin) != len(set(fixed_admin)):
        raise ValueError("AdminAddress must be distinct for every node.")
    return configs


def require_fresh_output(configs):
    """Only nodes about to start must have fresh outputs, not their peers."""
    for _, _, output in configs:
        if any((output / name).exists() for name in ("events.jsonl", "result.json", "running.lock")):
            raise ValueError(f"Existing node output at {output}; initialize a fresh cluster.")


def run_one_node(binary, path, expected_crash=False):
    """Keep this real Go node attached to the user's current terminal."""
    process = subprocess.Popen([str(binary), "-config", str(path)], cwd=REPO)
    try:
        try:
            code = process.wait()
        except KeyboardInterrupt:
            # The attached Go process also receives terminal Ctrl+C. Let it
            # save its result before falling back to forced cleanup.
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
            raise
        if code and not (expected_crash and code == 86):
            raise RuntimeError(f"Node exited with code {code}.")
    finally:
        stop_processes([process])


def wait_for_processes(processes, seconds):
    """Wait for all children against one shared deadline."""
    deadline = time.monotonic() + seconds
    while any(process.poll() is None for process in processes):
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.05)
    return True


def stop_processes(processes, graceful_seconds=0):
    """Stop only children launched by this script, concurrently and promptly."""
    if graceful_seconds and wait_for_processes(processes, graceful_seconds):
        return
    for process in processes:
        if process.poll() is None:
            process.terminate()
    if not wait_for_processes(processes, 3):
        for process in processes:
            if process.poll() is None:
                process.kill()
    for process in processes:
        if process.poll() is None:
            process.kill()
        process.wait()


def verify_results(configs, binary):
    expected_headers = None
    expected_root = None
    pids = set()
    counts = Counter()
    for path, config, output in configs:
        if config["Behavior"] != "honest":
            print(f"Node {config['NodeID']:2}: behavior={config['Behavior']} (excluded from honest agreement checks)")
            continue
        result = json.loads((output / "result.json").read_text(encoding="utf-8"))
        inspected = subprocess.run([str(binary), "-config", str(path), "-inspect"],
                                   capture_output=True, text=True, check=True, encoding="utf-8")
        ledger = json.loads(inspected.stdout) or []
        if (result["NodeID"] != config["NodeID"] or result["PID"] in pids
                or len(ledger) != result["Height"]):
            raise ValueError(f"Incomplete or invalid result for {path.name}")
        pids.add(result["PID"])
        headers = [entry["Block"]["Header"] for entry in ledger]
        if expected_headers is None:
            expected_headers, expected_root = headers, result["StateRoot"]
        elif headers != expected_headers or result["StateRoot"] != expected_root:
            raise ValueError(f"Nodes disagree at {path.name}")
        print(f"Node {result['NodeID']:2}: PID {result['PID']:6}, blocks {len(ledger)}")
        with (output / "events.jsonl").open(encoding="utf-8") as log:
            for line in log:
                counts[json.loads(line)["Event"]] += 1
    print(f"Matching state root: {expected_root}")
    for name in ("fault_drop", "fault_duplicate", "fault_reorder", "fault_replay",
                 "send_error", "block", "unblock"):
        if counts[name]:
            print(f"  {name}: {counts[name]}")
    print("On-disk honest histories and roots agree. Go tests additionally verify execution and quorum signatures.")


def run_cluster(binary, configs):
    directory = configs[0][2].parent
    if any(output.parent != directory for _, _, output in configs):
        raise ValueError("All node OutputDir values must share one runtime directory.")
    directory.mkdir(parents=True, exist_ok=True)
    processes = []
    interrupted = False
    try:
        duration = max(c[1]["RunForMS"] for c in configs)
        deadline = time.monotonic() + duration / 1000 + 20 if duration else None
        for path, _, _ in configs:
            processes.append(subprocess.Popen([str(binary), "-config", str(path)], cwd=REPO))
        pending = set(range(len(processes)))
        while pending:
            for index in tuple(pending):
                process = processes[index]
                code = process.poll()
                if code is None:
                    continue
                pending.remove(index)
                config = configs[index][1]
                if code and not (config["Behavior"] == "crashed_proposer" and code == 86):
                    raise RuntimeError(f"Node PID {process.pid} exited with code {code}.")
            if pending and deadline is not None and time.monotonic() >= deadline:
                raise RuntimeError(f"Cluster exceeded its shutdown deadline; inspect events.jsonl in {directory}.")
            if pending:
                time.sleep(0.1)  # Keeps Ctrl+C responsive on Windows.
        verify_results(configs, binary)
    except KeyboardInterrupt:
        interrupted = True
        print("Stopping all validator processes...", file=sys.stderr, flush=True)
        raise
    finally:
        # Shared-console children receive Ctrl+C too. Give them time to close
        # LevelDB and save result.json before forcing any survivor to stop.
        stop_processes(processes, graceful_seconds=5 if interrupted else 0)


def main():
    if len(sys.argv) == 1:
        print("Create config: python tests/run_local_cluster.py --init demo --f 3")
        print("Run network:   python tests/run_local_cluster.py --config-dir config/demo")
        print("More options:  python tests/run_local_cluster.py --help")
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable path")
    parser.add_argument("--f", type=int, default=DEFAULT_F,
                        help="init only: Byzantine tolerance; validator count N is 3F+1")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--init", help="Create config/<name>, then exit")
    group.add_argument("--config-dir", type=Path, help="Start existing, edited configs without regenerating")
    parser.add_argument("--submit", help="Submit this value to --node using its signing key")
    parser.add_argument("--nonce", type=int, default=1)
    parser.add_argument("--record", default="message")
    parser.add_argument("--via", type=int, help="Submit through another live node")
    parser.add_argument("--inspect", action="store_true", help="Read --node's LevelDB after it stops")
    actions = parser.add_mutually_exclusive_group()
    actions.add_argument("--node", type=int, help="Run only this node in the current terminal; requires --config-dir")
    actions.add_argument("--check-only", action="store_true", help="Check saved results without building or starting nodes; requires --config-dir")
    args = parser.parse_args()
    if os.name == "nt" and hasattr(signal, "SIGBREAK"):
        signal.signal(signal.SIGBREAK, handle_interrupt)
    if args.submit is not None and args.inspect:
        parser.error("Choose --submit or --inspect")
    init_forbidden = {"--submit", "--nonce", "--record", "--via", "--inspect", "--node", "--check-only"}
    supplied_options = {argument.split("=", 1)[0] for argument in sys.argv[1:] if argument.startswith("--")}
    if args.init and init_forbidden.intersection(supplied_options):
        parser.error("--init only accepts --f (and optional --go)")
    if args.config_dir and "--f" in supplied_options:
        parser.error("--f is only valid with --init; running uses F from network.json")
    if (args.node is not None or args.check_only) and not args.config_dir:
        parser.error("--node/--check-only require --config-dir")
    if args.init:
        directory = prepare_cluster(args.go, args.init, args.f)
        configs = load_configs(directory)
        print(f"Created {len(configs)} validator configs in: {directory}")
        print("Edit the JSON files, then run:")
        print(f'python tests/run_local_cluster.py --config-dir "{directory}"')
        return

    directory = args.config_dir.resolve()
    binary = REPO / "local-runs" / ("node.exe" if os.name == "nt" else "node")
    configs = load_configs(directory)
    print(f"Config directory: {directory}", flush=True)
    print(f"Runtime directory: {configs[0][2].parent}", flush=True)
    if args.check_only:
        verify_results(configs, binary)
    elif args.submit is not None or args.inspect:
        if args.node is None:
            raise ValueError("--submit/--inspect require --config-dir and --node")
        selected = [entry for entry in configs if entry[1]["NodeID"] == args.node]
        if not selected:
            raise ValueError("Unknown node")
        command = [str(binary), "-config", str(selected[0][0])]
        if args.inspect:
            command.append("-inspect")
        else:
            command += ["-submit", args.submit, "-nonce", str(args.nonce), "-record", args.record]
            if args.via is not None:
                command += ["-via", str(args.via)]
        subprocess.run(command, cwd=REPO, check=True)
    elif args.node is not None:
        selected = [entry for entry in configs if entry[1]["NodeID"] == args.node]
        if not selected:
            raise ValueError(f"Unknown node ID: {args.node}")
        require_fresh_output(selected)
        if not binary.exists():
            binary = build_node(args.go)
        run_one_node(binary, selected[0][0], selected[0][1]["Behavior"] == "crashed_proposer")
    else:
        require_fresh_output(configs)
        binary = build_node(args.go)
        run_cluster(binary, configs)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("Interrupted; started child processes were stopped.", file=sys.stderr)
        sys.exit(130)
