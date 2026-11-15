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
| `BaseTimeoutMS` | `2000` | Timeout cơ sở, tăng theo round (trong `network.json`) |
| `RetryMS` | `400` | Chu kỳ phát lại |
| `MempoolLimit` | `4096` | Transaction tối đa trong RAM |
| `MaxBlockTXs` | `128` | Transaction tối đa mỗi block (trong `network.json`) |
| `MaxBlockBytes` | `4177920` | Kích thước body tối đa (trong `network.json`) |
| `Trace` | `false` | Log network chi tiết |
| `ConsoleLog` | `false` | In event log ra terminal |

Hai tham số chung trong `network.json` điều khiển lúc proposer tạo block:

| Field | Default | Ý nghĩa |
|---|---:|---|
| `MinBlockTXs` | `3` | Tạo block ngay khi đủ transaction thực thi được |
| `BlockMaxWaitMS` | `2000` | Nếu chưa đủ, tạo block sau thời gian chờ tối đa |

Behavior hỗ trợ: `invalid_signature`, `wrong_domain`, `duplicate_transaction`,
`replay_transaction`, `silent_proposer`, `crashed_proposer`, `equivocate`
(ký hai vote xung đột cho mỗi vote; chỉ dùng tối đa `f` node).

Network delay/drop/duplicate/reorder/replay nằm trong object `Faults`; xem
`src/realnet/faults.go` để biết các field.

Để tạo testcase khác, chạy lại với tên mới rồi chỉ sửa JSON:

```text
python tests/run_local_cluster.py --init delay-test --f 3
```

Không dùng lại `OutputDir` cũ. Xóa `local-runs/<test>` nếu muốn bỏ output;
config testcase vẫn còn trong `config/<test>`.

## Kịch bản T1-T8 trên mạng thật

Mỗi kịch bản là một file Python độc lập trong `tests/scenarios/`, chạy không cần
tham số. File tự tạo `config/<tên>` nếu chưa có, chạy 10 node, spam 30
transaction bằng vòng `for` vào node 9, rồi kiểm tra: trạng thái cuối qua HTTP
admin (`/status`, `/ledger`) và các diễn biến ở giữa qua `events.jsonl`.

```text
python tests/scenarios/t1_normal.py
python tests/scenarios/t7_equivocation.py
```

| File | Kịch bản | Kết quả bắt buộc |
|---|---|---|
| `t1_normal.py` | Không lỗi | Mọi honest node cùng chain và state root |
| `t2_duplicate_reorder.py` | Duplicate 100%, delay, reorder | Vote trùng bị từ chối, không finalize xung đột |
| `t3a_invalid_signature.py`, `t3b_wrong_domain.py` | Node 0 ký sai / sai domain | Message bị từ chối, lý do được log |
| `t4_replay_duplicate_tx.py` | Node 0 replay, node 1 duplicate transaction | Mỗi `(sender, nonce)` chỉ áp dụng một lần |
| `t5_drop_before_sync.py` | Drop 100% trong 8 giây đầu | Có drop, sau đó chain tiếp tục, không double finalize |
| `t6a_silent_proposer.py`, `t6b_crashed_proposer.py` | Proposer node 0 im lặng / thoát | Height 1 xong ở round >= 1 nhờ timeout |
| `t7_equivocation.py` | Node 0, 1, 2 ký hai vote xung đột | Honest node log `conflicting vote`, vẫn đồng thuận |
| `t8_same_seed.py` | Seed lỗi cố định, chạy hai lần | Cùng transaction đã finalize và cùng state root |

File in `PASS`/`FAIL` cho từng kiểm tra và thoát với mã 1 nếu có lỗi. Config
sinh ra ở `config/t*-*` có thể sửa JSON rồi chạy lại. Output nằm ở
`local-runs/<tên>` và được xóa tự động trước mỗi lần chạy. Cần `go` trong PATH;
nếu Go cục bộ cũ hơn `go.mod`, đặt `GOTOOLCHAIN=auto`. `harness.py` chỉ chứa
phần chung: tạo config, chạy/dừng process, gửi transaction và đọc kết quả.
Log không giống từng byte giữa hai lần chạy T8 vì có timestamp, nên T8 so sánh
theo transaction đã finalize và state root.

Spam thủ công vào một validator đang chạy bằng một vòng `for` riêng:

```text
python tests/spam_transactions.py --config-dir config/t1-normal --via 9 --count 200
```

## Dữ liệu và kiểm tra

Mỗi node lưu finalized block trong `ledger.db/` bằng LevelDB và log trong
`events.jsonl`. Mempool chỉ nằm trong RAM. State không được lưu riêng mà được
dựng lại từ block.

## Full sync (khởi động lại từ ledger)

Đặt `"Recover": true` trong `node-XX.json` để node replay `ledger.db` khi
khởi động. Với từng block `1..H`, node kiểm tra lại header, quorum PRECOMMIT,
chữ ký transaction và `StateRoot` như khi nhận block từ peer, rồi mới vào
consensus ở height `H+1`. Block sai hoặc bị sửa trên đĩa làm node dừng với lỗi.
Các block còn thiếu được kéo từ peer bằng cơ chế decision header/body request.
Khi `Recover` bật, node ghi nối tiếp vào `events.jsonl` và ghi event
`ledger_recovered`. Không bật thì node từ chối chạy nếu thư mục output đã có dữ
liệu, như trước.

Giới hạn: voting/lock state của height đang dở chưa được persist, nên node vừa
restart có thể ký vote khác với vote nó đã gửi trước khi dừng. Hành vi này tính
như một Byzantine fault, nên chỉ restart tối đa `f` node cùng lúc. Nếu node bị
kill cứng, xóa `running.lock` trong thư mục output của node đó trước khi chạy lại.

Kiểm thử thủ công:

```text
python tests/run_local_cluster.py --init sync-test --f 3
python tests/run_local_cluster.py --config-dir config/sync-test
python tests/send_bulk_transactions.py --config-dir config/sync-test --via 9 --count 500 --rate 100
```

1. Chờ vài chục block, rồi dừng node 3 bằng Ctrl+C trong terminal của nó (chạy
   từng node riêng bằng `--node 3` để dễ dừng một node).
2. Ghi height và state root trong `local-runs/sync-test/node-03/result.json`.
3. Tiếp tục gửi transaction để mạng chạy thêm block khi node 3 vắng mặt.
4. Sửa `config/sync-test/node-03.json`, thêm `"Recover": true`, rồi chạy lại:
   `python tests/run_local_cluster.py --config-dir config/sync-test --node 3`.
5. Kỳ vọng: `events.jsonl` của node 3 có `ledger_recovered` với đúng height ở
   bước 2, sau đó node nhận các block còn thiếu và bắt kịp mạng. So sánh
   `/status` của node 3 (`http://127.0.0.1:31003/status`) với node khác: cùng
   height và cùng state root.
6. Kiểm tra lỗi: dừng node, sửa một byte trong `ledger.db` hoặc dùng ledger của
   chain khác, rồi chạy với `Recover`. Node phải báo `restore height N: ...` hoặc
   `ledger belongs to a different chain` và không join mạng.

```text
gofmt -w src
go test ./... -count=1
go vet ./...
```

Integration suite (Go test, vet, rồi lần lượt mọi file trong `tests/scenarios/`; `--race` chỉ áp dụng cho `go test`):

```text
python tests/run_tests.py
python tests/run_tests.py --race
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
