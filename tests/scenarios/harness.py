"""Plumbing shared by the T1-T8 scenario scripts: config, cluster process control, queries.

The scenario files themselves hold the fault setup, the spam loop and the checks.
"""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
from urllib.request import urlopen

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "tests"))
import run as launcher  # noqa: E402
from client import Node, RPCError, Wallet  # noqa: E402

FAILED = []


def check(condition, message):
    print(("  ok   " if condition else "  FAIL ") + message, flush=True)
    if not condition:
        FAILED.append(message)


def finish(name):
    print(f"RESULT {name}: {'PASS' if not FAILED else 'FAIL'}")
    sys.exit(1 if FAILED else 0)


def faults(seed, **policy):
    """Same Faults object on every node; the seed is offset by node ID."""
    return {"all": {"Faults": dict(Seed=seed, **policy)}}


class Cluster:
    """n=10, f=3 local cluster for one scenario, created under config/<name>."""

    def __init__(self, name, network=None, nodes=None, go=None):
        self.name, self.go, self.procs = name, go or os.environ.get("LAB01_GO", "go"), {}
        self._wallet, self._nodes = None, {}
        self.directory = REPO / "config" / name
        self.output = REPO / "local-runs" / name
        if not self.directory.exists():
            self._create(network or {}, nodes or {})
        self.configs = launcher.load_configs(self.directory)
        self.honest = [c["NodeID"] for _, c, _ in self.configs if c["Behavior"] == "honest"]
        if self.output.exists():
            shutil.rmtree(self.output)  # Only this scenario's own scratch output.

    def _create(self, network, nodes):
        subprocess.run([sys.executable, str(REPO / "tests" / "run.py"),
                        "--init", self.name, "--f", "3", "--go", self.go], cwd=REPO, check=True)
        shared = self.directory / "network.json"
        data = json.loads(shared.read_text(encoding="utf-8"))
        data.update(network)
        shared.write_text(json.dumps(data, indent=2), encoding="utf-8")
        for path in sorted(self.directory.glob("node-*.json")):
            config = json.loads(path.read_text(encoding="utf-8"))
            config["Trace"] = True  # Without it the node log is rate-limited, so event counts are samples.
            for key in ("all", config["NodeID"]):
                patch = json.loads(json.dumps(nodes.get(key, {})))
                if "Faults" in patch:
                    patch["Faults"]["Seed"] += config["NodeID"]
                config.update(patch)
            path.write_text(json.dumps(config, indent=2), encoding="utf-8")

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *exc):
        self.stop()

    def start(self):
        self.binary = launcher.build_node(self.go)
        for path, config, _ in self.configs:
            self.procs[config["NodeID"]] = subprocess.Popen([str(self.binary), "-config", str(path)], cwd=REPO)
        deadline = time.monotonic() + 120
        for node in self.honest:
            while True:
                try:
                    self.status(node)
                    break
                except OSError:
                    if time.monotonic() > deadline:
                        raise RuntimeError(f"node {node} never answered /status")
                    time.sleep(0.3)
        time.sleep(5)  # GossipSub meshes need a moment before the first transaction.

    def stop(self):
        for proc in self.procs.values():
            if proc.poll() is None:
                proc.send_signal(signal.SIGINT)
        deadline = time.monotonic() + 8
        for proc in self.procs.values():
            try:
                proc.wait(timeout=max(0.1, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()

    # --- client side -------------------------------------------------------
    def submit(self, nonce, key="spam", value=None, via=9):
        """Send `key` = `value` as transaction `nonce` to node `via`, signed by the external client key.

        Signing and the JSON-RPC call happen here (tests/client.py); the node binary is not
        involved. The client key is a fresh Ed25519 pair outside the validator set. It is created
        on the first call and kept in config/<name>/client-key.hex so reruns sign identical
        transactions."""
        value = f"spam-{nonce}" if value is None else value
        if self._wallet is None:
            network = json.loads((self.directory / "network.json").read_text(encoding="utf-8"))
            self._wallet = Wallet.load(self.directory / "client-key.hex",
                                       [peer["PublicKey"] for peer in network["Peers"]])
        if via not in self._nodes:
            port = self.configs[via][1]["AdminAddress"].rpartition(":")[2]
            self._nodes[via] = Node(f"127.0.0.1:{port}")  # AdminAddress may be 0.0.0.0
        deadline = time.monotonic() + 60
        while True:
            try:
                self._nodes[via].send(self._wallet, key, value, nonce)
                return
            except RPCError as error:
                # A full mempool is back-pressure, not a rejection: wait for blocks to drain it.
                transient = "exhausted" in error.message or "capacity exceeded" in error.message
                if not transient or time.monotonic() > deadline:
                    raise RuntimeError(f"nonce {nonce} rejected by node {via}: {error}") from error
                time.sleep(0.2)
            except OSError as error:
                raise RuntimeError(f"nonce {nonce} rejected by node {via}: {error}") from error

    # --- queries (admin HTTP while the cluster runs, events.jsonl for the middle) ---
    def _get(self, node, path):
        address = self.configs[node][1]["AdminAddress"]
        with urlopen(f"http://{address}{path}", timeout=5) as response:
            return json.load(response)

    def status(self, node):
        return self._get(node, "/status")

    def ledger(self, node):
        return self._get(node, "/ledger")["Blocks"] or []

    def transactions(self, node):
        return [tx for f in self.ledger(node) for tx in f["Block"]["Body"]["Transactions"] or []]

    def events(self, node):
        rows = []
        path = self.configs[node][2] / "events.jsonl"
        for line in path.read_text(encoding="utf-8").splitlines():
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                pass  # The node may be mid-write on the last line.
        return rows

    def wait_finalized(self, count, nodes=None, timeout=90):
        """Wait until every listed node has finalized `count` transactions and caught up."""
        nodes = self.honest if nodes is None else nodes
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if all(len(self.transactions(n)) >= count for n in nodes):
                    break
            except OSError:
                pass
            time.sleep(0.5)
        time.sleep(1)  # Let the last decision reach slower nodes.

    def check_agreement(self, count):
        """Safety: one history, one root, no transaction applied twice."""
        nodes = self.honest
        statuses = {n: self.status(n) for n in nodes}
        ledgers = {n: self.ledger(n) for n in nodes}
        headers = {json.dumps([f["Block"]["Header"] for f in ledgers[n]], sort_keys=True) for n in nodes}
        check(len(headers) == 1, f"{len(nodes)} honest nodes hold byte-identical chains (height {statuses[nodes[0]]['Height']})")
        check(len({s["StateRoot"] for s in statuses.values()}) == 1, "all honest nodes report the same state root")
        keys = [(json.dumps(t["Sender"]), t["Nonce"]) for t in self.transactions(nodes[0])]
        check(len(keys) == len(set(keys)), "no (sender, nonce) is applied twice")
        check(len(keys) == count, f"all {count} spam transactions finalized once (found {len(keys)})")
        return statuses[nodes[0]]["StateRoot"], keys

    def check_quorum_proofs(self, quorum=7):
        """No double-counted votes: every finalized block is carried by >= quorum PRECOMMITs
        for that block from distinct validators (a duplicated vote would repeat a validator)."""
        bad, blocks = [], 0
        for node in self.honest:
            for f in self.ledger(node):
                blocks += 1
                height = f["Block"]["Header"]["Height"]
                voters = [json.dumps(v["Validator"]) for v in f["Proof"]]
                same = all(v["Phase"] == 2 and not v["IsNil"] and v["Height"] == height
                           and v["BlockHash"] == f["Proof"][0]["BlockHash"] for v in f["Proof"])
                if len(voters) != len(set(voters)) or len(set(voters)) < quorum or not same:
                    bad.append((node, height, len(voters), len(set(voters))))
        check(blocks > 0 and not bad,
              f"every finalized block has >= {quorum} PRECOMMITs from distinct validators, none counted twice"
              + (f" (violations: {bad[:3]})" if bad else f" ({blocks} block proofs checked)"))

    def check_no_double_finalization(self):
        """Safety at every moment, not only at the end: from the event logs, no honest node
        committed a height twice and no two honest nodes committed different states at a height."""
        roots, repeated = {}, []
        for node in self.honest:
            seen = set()
            for row in self.events(node):
                if row["Event"] != "state_commit":
                    continue
                if row["Height"] in seen:
                    repeated.append((node, row["Height"]))
                seen.add(row["Height"])
                roots.setdefault(row["Height"], set()).add(row["StateRoot"])
        check(not repeated, f"no honest node finalized the same height twice{' ' + str(repeated[:3]) if repeated else ''}")
        split = sorted(h for h, found in roots.items() if len(found) != 1)
        check(roots and not split, f"each of the {len(roots)} heights was finalized with exactly one state root across honest nodes"
              + (f" (conflicts at {split[:3]})" if split else ""))

    def reasons(self, nodes=None):
        """Reject reasons logged by the given nodes (default: honest) -> {reason: count}."""
        found = {}
        for node in self.honest if nodes is None else nodes:
            for row in self.events(node):
                reason = row.get("Detail") or row.get("Reason")
                if "reject" in row["Event"] and reason:
                    found[reason] = found.get(reason, 0) + 1
        return found

    def count(self, event, nodes=None):
        return sum(1 for n in (self.honest if nodes is None else nodes)
                   for row in self.events(n) if row["Event"] == event)
