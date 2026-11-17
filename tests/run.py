"""Create validator configs or run all/one real Go node. Python 3.9+."""

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


DEFAULT_F = 3
DEFAULT_MEMPOOL_TTL_MS = 60_000
DEFAULT_ACCOUNT_SLOTS = 16
DEFAULT_ACCOUNT_QUEUE = 64
DEFAULT_GLOBAL_SLOTS = 1024
DEFAULT_GLOBAL_QUEUE = 256

REPO = Path(__file__).resolve().parent.parent
CRASH_EXIT_CODE = 86  # runtime.CrashExitCode: expected exit of a crashed_proposer node.


def handle_interrupt(_signum, _frame):
    raise KeyboardInterrupt


def binary_path():
    return REPO / "local-runs" / ("node.exe" if os.name == "nt" else "node")


def build_node(go):
    binary = binary_path()
    binary.parent.mkdir(exist_ok=True)
    subprocess.run([go, "build", "-buildvcs=false", "-o", str(binary), "./src/cmd/node"],
                   cwd=REPO, check=True)
    return binary


def binary_is_stale(binary):
    if not binary.is_file():
        return True
    inputs = [REPO / "go.mod", REPO / "go.sum"]
    inputs.extend(path for path in (REPO / "src").rglob("*") if path.is_file())
    return any(path.stat().st_mtime > binary.stat().st_mtime for path in inputs)


def prepare_cluster(go, name, fault_tolerance, mempool_ttl_ms, account_slots,
                    account_queue, global_slots, global_queue):
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
               "-init-output-dir", relative_output,
               "-mempool-ttl-ms", str(mempool_ttl_ms),
               "-account-slots", str(account_slots),
               "-account-queue", str(account_queue),
               "-global-slots", str(global_slots),
               "-global-queue", str(global_queue)]
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


def admin_port(path, config, output=None):
    """HTTP admin port of a node: the address the running process actually bound
    (written to <OutputDir>/admin), else the configured AdminAddress.
    Returns (port, source path)."""
    if output is None:
        output = Path(config.get("OutputDir", ""))
        if not output.is_absolute():
            output = (path.parent / output).resolve()
    runtime_address = output / "admin"
    if runtime_address.is_file():
        address, source = runtime_address.read_text(encoding="utf-8").strip(), runtime_address
    else:
        address, source = config.get("AdminAddress", ""), path
    _, separator, port = address.rpartition(":")
    if not separator or not port.isdigit() or port == "0":
        raise ValueError(f"{path.name}: no running HTTP address found; start the node first")
    return int(port), source


def admin_url(path, config, output=None):
    """Loopback URL of a node's admin HTTP server (AdminAddress may be 0.0.0.0)."""
    return f"http://127.0.0.1:{admin_port(path, config, output)[0]}"


def allowed_exit(code, config):
    return code == 0 or (config["Behavior"] == "crashed_proposer" and code == CRASH_EXIT_CODE)


def describe_exit(code):
    """Readable exit status; negative codes mean "killed by signal" on POSIX."""
    if code < 0:
        try:
            return f"was killed by {signal.Signals(-code).name}"
        except ValueError:
            return f"was killed by signal {-code}"
    return f"exited with code {code}"


def start_nodes(binary, configs):
    """Start one node process per (path, config, output) entry."""
    return [subprocess.Popen([str(binary), "-config", str(path)], cwd=REPO) for path, _, _ in configs]


def interrupt_processes(processes, seconds=8):
    """Graceful stop: SIGINT lets nodes close LevelDB and write result.json."""
    for process in processes:
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
    deadline = time.monotonic() + seconds
    for process in processes:
        try:
            process.wait(timeout=max(0.1, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def require_fresh_output(configs):
    """Only nodes about to start must have fresh outputs, not their peers."""
    for _, config, output in configs:
        if config.get("Recover"):
            continue
        if any((output / name).exists() for name in ("events.jsonl", "result.json", "running.lock")):
            raise ValueError(f"Existing node output at {output}; initialize a fresh cluster.")


def run_one_node(binary, path, expected_crash=False):
    process = subprocess.Popen([str(binary), "-config", str(path)], cwd=REPO)
    try:
        try:
            code = process.wait()
        except KeyboardInterrupt:
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
            raise
        if code and not (expected_crash and code == CRASH_EXIT_CODE):
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
        processes.extend(start_nodes(binary, configs))
        pending = set(range(len(processes)))
        failed = []
        while pending:
            for index in tuple(pending):
                process = processes[index]
                code = process.poll()
                if code is None:
                    continue
                pending.remove(index)
                if not allowed_exit(code, configs[index][1]):
                    # A dead validator is just a crash fault: the others keep
                    # running (up to f failures are tolerated by consensus).
                    node = configs[index][1]["NodeID"]
                    failed.append(f"node {node} ({describe_exit(code)})")
                    print(f"Warning: node {node} (PID {process.pid}) {describe_exit(code)}; "
                          f"{len(pending)} validator(s) still running. See {configs[index][2] / 'events.jsonl'}",
                          file=sys.stderr, flush=True)
            if pending and deadline is not None and time.monotonic() >= deadline:
                raise RuntimeError(f"Cluster exceeded its shutdown deadline; inspect events.jsonl in {directory}.")
            if pending:
                time.sleep(0.1)  # Keeps Ctrl+C responsive on Windows.
        if failed:
            raise RuntimeError(f"Validators exited unexpectedly: {', '.join(failed)}.")
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
        print("Create config: python tests/run.py --init demo --f 3")
        print("Run network:   python tests/run.py --config-dir config/demo")
        print("Run one node:  python tests/run.py --config-dir config/demo --node 0")
        print("More options:  python tests/run.py --help")
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable path")
    parser.add_argument("--f", type=int, default=DEFAULT_F,
                        help="init only: Byzantine tolerance; validator count N is 3F+1")
    parser.add_argument("--mempool-ttl-ms", type=int, default=DEFAULT_MEMPOOL_TTL_MS,
                        help="init only: transaction TTL (default: 60000)")
    parser.add_argument("--account-slots", type=int, default=DEFAULT_ACCOUNT_SLOTS,
                        help="init only: protected slots per account (default: 16)")
    parser.add_argument("--account-queue", type=int, default=DEFAULT_ACCOUNT_QUEUE,
                        help="init only: extra queue per account (default: 64)")
    parser.add_argument("--global-slots", type=int, default=DEFAULT_GLOBAL_SLOTS,
                        help="init only: total transaction slots (default: 1024)")
    parser.add_argument("--global-queue", type=int, default=DEFAULT_GLOBAL_QUEUE,
                        help="init only: total extra queue (default: 256)")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--init", help="Create config/<name>, then exit")
    group.add_argument("--config-dir", type=Path, help="Start existing, edited configs without regenerating")
    parser.add_argument("--node", type=int, help="Run only this node in the current terminal")
    args = parser.parse_args()
    if os.name == "nt" and hasattr(signal, "SIGBREAK"):
        signal.signal(signal.SIGBREAK, handle_interrupt)
    supplied_options = {argument.split("=", 1)[0] for argument in sys.argv[1:] if argument.startswith("--")}
    if args.init and args.node is not None:
        parser.error("--node requires --config-dir")
    if args.config_dir and "--f" in supplied_options:
        parser.error("--f is only valid with --init; running uses F from network.json")
    mempool_options = {"--mempool-ttl-ms", "--account-slots", "--account-queue",
                       "--global-slots", "--global-queue"}
    if args.config_dir and mempool_options.intersection(supplied_options):
        parser.error("Mempool flags are only valid with --init; running loads network.json")
    if args.init:
        directory = prepare_cluster(args.go, args.init, args.f, args.mempool_ttl_ms,
                                    args.account_slots, args.account_queue,
                                    args.global_slots, args.global_queue)
        configs = load_configs(directory)
        print(f"Created {len(configs)} validator configs in: {directory}")
        print("Edit the JSON files, then run:")
        print(f'python tests/run.py --config-dir "{directory}"')
        return

    directory = args.config_dir.resolve()
    configs = load_configs(directory)
    print(f"Config directory: {directory}", flush=True)
    binary = binary_path()
    if args.node is not None:
        selected = [entry for entry in configs if entry[1]["NodeID"] == args.node]
        if not selected:
            raise ValueError(f"Unknown node ID: {args.node}")
        print(f"Runtime directory: {selected[0][2].parent}", flush=True)
        require_fresh_output(selected)
        if binary_is_stale(binary):
            binary = build_node(args.go)
        run_one_node(binary, selected[0][0], selected[0][1]["Behavior"] == "crashed_proposer")
        return
    print(f"Runtime directory: {configs[0][2].parent}", flush=True)
    require_fresh_output(configs)
    run_cluster(build_node(args.go), configs)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("Interrupted; started child processes were stopped.", file=sys.stderr)
        sys.exit(130)
