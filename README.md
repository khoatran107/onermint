# Blockchain Lab01

Blockchain permissioned chạy bằng các process Go riêng trên localhost.
Validator set cố định, `n = 3f + 1`; mặc định `n=10`, `f=3`, quorum `7`.

```text
UI/bulk client --JSON-RPC--> mempool RAM
                                 |
                     GossipSub: TX, vote, header
                     gRPC bi-stream: block body
                                 |
                  PREVOTE -> PRECOMMIT -> LevelDB
```

Node không tự tạo transaction hoặc block rỗng. Execution và consensus nằm trong
cùng process vì lab không có staking.

## Chạy mạng

Yêu cầu: Python 3.9+, Go theo `go.mod`; Docker Desktop nếu dùng Grafana.

Tạo testcase mới. Config nằm trong `config/`, output nằm trong `local-runs/`:

```text
python tests/run_local_cluster.py --init template --f 3
```

```text
config/template/       network.json, node-*.json, key test
local-runs/template/   LevelDB, log và kết quả runtime
```

Chạy toàn bộ mạng:

```text
python tests/run_local_cluster.py --config-dir config/template
```

Hoặc chạy mỗi node trong một terminal riêng:

```text
python tests/run_local_cluster.py --config-dir config/template --node 0
python tests/run_local_cluster.py --config-dir config/template --node 1
```

Tiếp tục đến node 9. Ba range cách nhau 1000 port nên không đổi khi thay `n`:
gRPC `29000..`, GossipSub `30000..`, HTTP/UI `31000..`.

## Gửi transaction

Mở UI của một honest node, ví dụ `http://127.0.0.1:31009/`.

Người dùng chỉ nhập `Key` và `Value`. Browser tự tạo ví Ed25519 test-only, thêm
owner, lấy nonce, ký transaction và gửi JSON-RPC. Private key không rời browser.

Bulk client dùng cùng JSON-RPC:

```text
python tests/send_bulk_transactions.py --config-dir config/template --via 9 --count 20000 --rate 200
```

`--via` phải là honest node. Script chỉ gửi transaction; proposer tự chọn theo
`MaxBlockTXs` và `MaxBlockBytes`.

## Grafana

Khi validator đang chạy:

```text
python tests/prepare_monitoring.py --config-dir config/template
docker-compose -f logs/monitoring/docker-compose.yml up -d
```

Mở `http://127.0.0.1:3000`, đăng nhập `admin / lab01`. Dashboard hiển thị
height, mempool, consensus round/step, fault và log của từng node.

```text
docker-compose -f logs/monitoring/docker-compose.yml down
```

API debug: `/status`, `/mempool`, `/ledger`, `/metrics`. JSON-RPC nằm tại
`POST /rpc`.

## Config node

Ví dụ `config/template/node-00.json`:

```json
{
  "NodeID": 0,
  "NetworkFile": "network.json",
  "OutputDir": "../../local-runs/template/node-00",
  "Behavior": "honest",
  "AdminAddress": "127.0.0.1:31000",
  "SendGapMS": 1
}
```

Thông số thường sửa:

| Field | Default | Ý nghĩa |
|---|---:|---|
| `Behavior` | `honest` | Hành vi validator |
| `SendGapMS` | `1` | Giới hạn outbound |
| `BaseTimeoutMS` | `2000` | Timeout cơ sở, tăng theo round |
| `RetryMS` | `400` | Chu kỳ phát lại |
| `MempoolLimit` | `4096` | Transaction tối đa trong RAM |
| `MaxBlockTXs` | `128` | Transaction tối đa mỗi block |
| `MaxBlockBytes` | `4177920` | Kích thước body tối đa |
| `Trace` | `false` | Log network chi tiết |
| `ConsoleLog` | `false` | In event log ra terminal |

Hai tham số chung trong `network.json` điều khiển lúc proposer tạo block:

| Field | Default | Ý nghĩa |
|---|---:|---|
| `MinBlockTXs` | `3` | Tạo block ngay khi đủ transaction thực thi được |
| `BlockMaxWaitMS` | `2000` | Nếu chưa đủ, tạo block sau thời gian chờ tối đa |

Behavior hỗ trợ: `invalid_signature`, `wrong_domain`, `duplicate_transaction`,
`replay_transaction`, `silent_proposer`, `crashed_proposer`.

Network delay/drop/duplicate/reorder/replay nằm trong object `Faults`; xem
`src/realnet/faults.go` để biết các field.

Để tạo testcase khác, chạy lại với tên mới rồi chỉ sửa JSON:

```text
python tests/run_local_cluster.py --init delay-test --f 3
```

Không dùng lại `OutputDir` cũ. Xóa `local-runs/<test>` nếu muốn bỏ output;
config testcase vẫn còn trong `config/<test>`.

## Dữ liệu và kiểm tra

Mỗi node lưu finalized block trong `ledger.db/` bằng LevelDB và log trong
`events.jsonl`. Mempool chỉ nằm trong RAM. Restart recovery chưa được hỗ trợ vì
signing/lock state chưa được persist.

```text
gofmt -w src
go test ./... -count=1
go vet ./...
```

Integration suite:

```text
python tests/run_tests.py
python tests/run_tests.py --race
python tests/verify_repeatability.py
```

| Thư mục | Trách nhiệm |
|---|---|
| `config` | Config testcase và test key |
| `local-runs` | Binary, ledger, log và output tạm |
| `src/crypto`, `src/types` | Ed25519, SHA-256, canonical encoding |
| `src/state`, `src/block` | Ownership, nonce, execution, Merkle root |
| `src/mempool`, `src/ledger` | RAM mempool và LevelDB ledger |
| `src/consensus` | Propose, PREVOTE, PRECOMMIT, finality |
| `src/runtime`, `src/realnet` | Node runtime, GossipSub, gRPC, JSON-RPC và fault |
| `logs` | Log runtime và bộ Grafana/Prometheus/Loki/Alloy |
