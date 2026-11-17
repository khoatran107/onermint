"""Plumbing shared by the T1-T8 scenario scripts: config, cluster process control, queries.

A scenario file holds only its fault setup and its specific checks; `scenario()` runs the
common flow: start the cluster, submit COUNT transactions, wait, check agreement.
"""

import json
import os
from pathlib import Path
import shutil
import sys
import time
from urllib.request import urlopen

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "tests"))
import run as launcher  # noqa: E402
from client_lib import Node, RPCError, Wallet  # noqa: E402

FAILED = []
DEFAULT_NETWORK = {"BaseTimeoutMS": 1000}  # Short rounds keep fault scenarios quick.
DEFAULT_COUNT = 30
SUBMIT_VIA = 9  # The last node is honest in every scenario.


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


def behaviors(behavior, *nodes):
    """Give the listed node IDs one Byzantine behaviour."""
    return {node: {"Behavior": behavior} for node in nodes}


def run(name, network=None, nodes=None, count=DEFAULT_COUNT, wait_nodes=None, timeout=90, checks=None):
    """One cluster run: submit `count` transactions, wait, check agreement, then `checks(net)`.

    Returns check_agreement's (state root, [(sender, nonce)])."""
    with Cluster(name, DEFAULT_NETWORK if network is None else network, nodes) as net:
        net.spam(count)
        net.wait_finalized(count, nodes=wait_nodes, timeout=timeout)
        result = net.check_agreement(count)
        if checks:
            checks(net)
    return result


def scenario(name, **kwargs):
    """`run` once and exit with the scenario's PASS/FAIL status."""
    run(name, **kwargs)
    finish(name)


# --- checks shared by more than one scenario --------------------------------
def check_signature_rejected(net, faulty=(0,)):
    """T3: the faulty node really misbehaved and honest nodes logged why they rejected it."""
    check(net.count("behavior_message", nodes=list(faulty)) > 0, f"node {', '.join(map(str, faulty))} really sent faulty messages")
    reasons = net.reasons()
    check(any("signature" in r for r in reasons),
          f"bad signatures were rejected with a logged reason: {dict(sorted(reasons.items())[:4])}")


def check_timeout_recovered(net):
    """T6: height 1 had no usable proposal in round 0, so it was decided later."""
    first = net.ledger(SUBMIT_VIA)[0]
    check(first["Round"] >= 1, f"height 1 could only finish after a timeout (decided in round {first['Round']})")


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
        launcher.prepare_cluster(self.go, self.name, 3, launcher.DEFAULT_MEMPOOL_TTL_MS,
                                 launcher.DEFAULT_ACCOUNT_SLOTS, launcher.DEFAULT_ACCOUNT_QUEUE,
                                 launcher.DEFAULT_GLOBAL_SLOTS, launcher.DEFAULT_GLOBAL_QUEUE)
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
        binary = launcher.build_node(self.go)
        for (_, config, _), proc in zip(self.configs, launcher.start_nodes(binary, self.configs)):
            self.procs[config["NodeID"]] = proc
        deadline = time.monotonic() + 120
        for node in self.honest:
            while True:
                try:
                    self.status(node)
                    break
                except (OSError, ValueError):  # ValueError: admin address not written yet.
                    if time.monotonic() > deadline:
                        raise RuntimeError(f"node {node} never answered /status")
                    time.sleep(0.3)
        time.sleep(5)  # GossipSub meshes need a moment before the first transaction.

    def stop(self):
        launcher.interrupt_processes(list(self.procs.values()))

    def _url(self, node):
        return launcher.admin_url(*self.configs[node])

    # --- client side -------------------------------------------------------
    def spam(self, count, via=SUBMIT_VIA):
        """Submit transactions 1..count, each setting "spam" = "value-<nonce>"."""
        for nonce in range(1, count + 1):
            self.submit(nonce, key="spam", value=f"value-{nonce}", via=via)

    def submit(self, nonce, key="spam", value=None, via=SUBMIT_VIA):
        """Send `key` = `value` as transaction `nonce` to node `via`, signed by the external client key.

        Signing and the JSON-RPC call happen here (tests/client_lib.py); the node binary is not
        involved. The client key is a fresh Ed25519 pair outside the validator set. It is created
        on the first call and kept in config/<name>/client-key.hex so reruns sign identical
        transactions."""
        value = f"spam-{nonce}" if value is None else value
        if self._wallet is None:
            network = json.loads((self.directory / "network.json").read_text(encoding="utf-8"))
            self._wallet = Wallet.load(self.directory / "client-key.hex",
                                       [peer["PublicKey"] for peer in network["Peers"]])
        if via not in self._nodes:
            self._nodes[via] = Node(self._url(via))
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
        with urlopen(self._url(node) + path, timeout=5) as response:
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

    def votes_signed_per_round(self, node):
        """{(height, round): number of votes this node signed}."""
        signed = {}
        for row in self.events(node):
            if row["Event"] == "consensus_vote_signed":
                key = (row["Height"], row.get("Round", 0))
                signed[key] = signed.get(key, 0) + 1
        return signed
