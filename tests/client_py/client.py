"""Scripted client: tự tay viết từng transaction trong main() rồi chạy

    python tests/client_py/client.py
    NODE_URL=http://192.168.1.10:31003 python tests/client_py/client.py

Dùng tests/client_lib.py (ký Ed25519 + JSON-RPC). Cần `pip install cryptography`.
"""

import json
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # tests/, where client_lib.py lives
from client_lib import Node, RPCError, Wallet  # noqa: E402

DEFAULT_NODE_URL = "http://127.0.0.1:31000"
WALLET_DIR = Path(__file__).resolve().parent / "wallets"  # mỗi ví một file hex(seed||pubkey), test-only


def wallet(name):
    """Ví tên `name`; tạo lần đầu rồi lưu vào tests/client_py/wallets/<name>.hex. Mỗi tên là một Sender khác nhau."""
    if not name.replace("_", "").replace("-", "").isalnum():
        raise ValueError("tên ví chỉ gồm chữ, số, '_' và '-'")
    WALLET_DIR.mkdir(exist_ok=True)
    return Wallet.load(WALLET_DIR / f"{name}.hex")


def node_url(url=None):
    return url or os.environ.get("NODE_URL") or DEFAULT_NODE_URL


def send(w, key, value, nonce=None, node=None, mutate=None, dry_run=False):
    """Ký và gửi một transaction `key` = `value` của ví `w`.

    nonce    ép nonce; mặc định lấy nextNonce từ node (đã tính cả tx đang chờ trong mempool)
    node     URL node nhận tx; mặc định $NODE_URL
    mutate   hàm (tx) -> None sửa tx SAU KHI ký (ví dụ làm hỏng chữ ký); tx là dict dạng gửi đi
    dry_run  True: chỉ ký và trả tx, không gửi
    """
    if not key or "/" in key:
        raise ValueError("key phải khác rỗng và không chứa '/'")
    n = Node(node_url(node))
    nonce = n.next_nonce(w) if nonce is None else nonce
    tx = w.sign_transaction(n.chain_id, key, value, nonce)
    if mutate:
        mutate(tx)
    if dry_run:
        return {"dryRun": True, "node": n.url, "tx": tx}
    return {"transactionID": n.call("lab_sendRawTransaction", transaction=tx),
            "nonce": nonce, "targetKey": tx["TargetKey"], "node": n.url}


def status(node=None):
    return Node(node_url(node)).call("lab_status")


def attempt(label, w, key, value, **kwargs):
    """Gửi và in kết quả; lỗi của một tx không dừng cả script (để thử tx cố tình sai)."""
    try:
        print(f"[{label}]", json.dumps(send(w, key, value, **kwargs)), flush=True)
    except (RPCError, OSError, ValueError) as error:
        print(f"[{label}] LỖI: {error}", flush=True)


# ======================================================================
# VIẾT TỪNG TRANSACTION Ở ĐÂY
# ======================================================================
def main():
    alice = wallet("alice")
    print("alice:", alice.public_hex)

    for i in range(10000):
        attempt(f"tx{i + 1}", alice, "message", "hello blockchain")

    # Ví khác; nonce của mỗi ví đếm riêng.
    # attempt("bob", bob, "message", "from bob")

    # Gửi tới một node cụ thể thay vì $NODE_URL.
    # attempt("node5", alice, "x", "y", node="http://127.0.0.1:31005")

    # Ép nonce (nhảy cóc để thử queue, hoặc trùng nonce để thử bị từ chối).
    # attempt("gap", alice, "gap", "nonce 100", nonce=100)

    # Làm hỏng chữ ký sau khi ký, node phải từ chối.
    # def corrupt(tx): tx["Signature"][0] ^= 1
    # attempt("bad",c alice, "bad", "x", mutate=corrupt)

    # Chỉ ký và in JSON, không gửi.
    # print(send(alice, "k", "v", dry_run=True))

    time.sleep(3)
    print("status:", json.dumps(status(), indent=2))


if __name__ == "__main__":
    try:
        main()
    except (RPCError, OSError) as error:
        sys.exit(f"Lỗi: {error}")
