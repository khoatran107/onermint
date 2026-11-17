"""Standalone transaction client for the test harness (signs locally, talks JSON-RPC).

It signs transactions itself and talks to a node only over JSON-RPC (`POST /rpc`), exactly
like the browser client. The node binary is a validator and nothing else.

Needs the `cryptography` package for Ed25519 (`pip install cryptography`).
"""

import base64
import json
import os
import struct
from pathlib import Path
from urllib.request import Request, urlopen

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


def _blob(data):
    return struct.pack(">I", len(data)) + data


def canonical_tx_bytes(chain_id, sender, target_key, value, nonce):
    """Must stay byte-identical to crypto.CanonicalTxBytes in Go and tests/client_web/app.js."""
    return (b"\x01" + _blob(f"TX:{chain_id}".encode()) + sender
            + _blob(target_key.encode()) + _blob(value) + struct.pack(">Q", nonce))


class Wallet:
    """One Ed25519 account. Stored as hex(seed || public key), 64 bytes, mode 0600."""

    def __init__(self, key):
        self.key = key
        self.public = key.public_key().public_bytes(
            serialization.Encoding.Raw, serialization.PublicFormat.Raw)
        self.public_hex = self.public.hex()

    @classmethod
    def load(cls, path, forbidden_public_keys=()):
        """Load the key at `path`, creating a fresh one if the file is missing.

        A key that belongs to the validator set is refused: clients are external accounts."""
        path = Path(path)
        if path.exists():
            raw = bytes.fromhex(path.read_text(encoding="utf-8").strip())
            if len(raw) != 64:
                raise ValueError(f"{path}: invalid client key file")
            wallet = cls(Ed25519PrivateKey.from_private_bytes(raw[:32]))
            if wallet.public != raw[32:]:
                raise ValueError(f"{path}: seed and public key do not match")
        else:
            wallet = cls(Ed25519PrivateKey.generate())
            seed = wallet.key.private_bytes(
                serialization.Encoding.Raw, serialization.PrivateFormat.Raw,
                serialization.NoEncryption())
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write((seed + wallet.public).hex())
        if wallet.public_hex in {k.lower() for k in forbidden_public_keys}:
            raise ValueError("client key must not be a validator key")
        return wallet

    def sign_transaction(self, chain_id, key, value, nonce):
        """Return the JSON-RPC wire form of the signed transaction `key` = `value`."""
        value = value.encode() if isinstance(value, str) else bytes(value)
        target_key = f"{self.public_hex}/{key}"
        signature = self.key.sign(canonical_tx_bytes(chain_id, self.public, target_key, value, nonce))
        return {"ChainID": chain_id, "Sender": list(self.public), "TargetKey": target_key,
                "Value": base64.b64encode(value).decode(), "Nonce": nonce,
                "Signature": list(signature)}


class RPCError(RuntimeError):
    def __init__(self, code, message):
        super().__init__(f"{code}: {message}")
        self.code, self.message = code, message


class Node:
    """JSON-RPC 2.0 connection to one node, e.g. Node("http://127.0.0.1:31009")."""

    def __init__(self, url, timeout=5):
        self.url = (url if "://" in url else "http://" + url).rstrip("/") + "/rpc"
        self.timeout, self._id, self._chain_id = timeout, 0, None

    def call(self, method, **params):
        self._id += 1
        body = json.dumps({"jsonrpc": "2.0", "id": self._id, "method": method, "params": params})
        request = Request(self.url, body.encode(), {"Content-Type": "application/json"})
        with urlopen(request, timeout=self.timeout) as response:
            reply = json.load(response)
        if reply.get("error"):
            raise RPCError(reply["error"]["code"], reply["error"]["message"])
        return reply["result"]

    @property
    def chain_id(self):
        if self._chain_id is None:
            self._chain_id = self.call("lab_chainId")
        return self._chain_id

    def next_nonce(self, wallet):
        return self.call("lab_getAccount", sender=wallet.public_hex)["nextNonce"]

    def send(self, wallet, key, value, nonce=None):
        """Sign and submit one transaction; returns its ID once the node's mempool admitted it.

        Admission is not finality. `nonce` defaults to the node's next free nonce."""
        nonce = self.next_nonce(wallet) if nonce is None else nonce
        tx = wallet.sign_transaction(self.chain_id, key, value, nonce)
        return self.call("lab_sendRawTransaction", transaction=tx)
