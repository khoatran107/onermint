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
python tests/run.py --init template --f 3
```

Có thể đổi giới hạn mempool ngay khi khởi tạo, ví dụ:

```text
python tests/run.py --init custom --f 3 --mempool-ttl-ms 60000 --account-slots 16 --account-queue 64 --global-slots 1024 --global-queue 256
```

```text
config/template/       network.json, node-*.json, key test
local-runs/template/   LevelDB, log và kết quả runtime
```

Chạy toàn bộ mạng:

```text
python tests/run.py --config-dir config/template
```

Không truyền `--node` thì script khởi động toàn bộ validator set. Để chạy từng
node trong terminal riêng:

```text
python tests/run.py --config-dir config/template --node 0
python tests/run.py --config-dir config/template --node 1
```

Ba range cách nhau 1000 port nên không đổi khi thay `n`:
gRPC `29000..`, GossipSub `30000..`, HTTP (JSON-RPC, `/metrics`, ...) `31000..`.

## Gửi transaction

Client là một web app tĩnh độc lập trong `client/`; node không còn serve UI.
Chạy client bằng một static server bất kỳ (cần `localhost` hoặc HTTPS để dùng
WebCrypto Ed25519):

```bash
python3 -m http.server 8080 --directory client
```

Mở `http://localhost:8080/`, rồi nhập URL của node muốn tương tác vào ô
**Node URL**, ví dụ `http://127.0.0.1:31009`. URL được nhớ trong browser và có
thể đổi sang node khác bất cứ lúc nào.

### Deploy client lên Vercel

`client/` là static web, không có build step. Deploy bằng Vercel CLI:

```text
vercel --cwd client
vercel --cwd client --prod
```

Nếu kết nối Git trong Vercel Dashboard, đặt **Root Directory** là `client`,
Framework Preset là **Other** và để Build Command trống.

Để máy khác trong LAN truy cập JSON-RPC, đặt node tương ứng bind mọi network
interface, ví dụ trong `node-00.json`:

```json
"AdminAddress": "0.0.0.0:31000"
```

Trong web client phải nhập IP LAN thật, ví dụ `http://192.168.1.66:31000`,
không nhập `0.0.0.0`. Cho phép port đó qua Windows Firewall ở profile Private.
Trang Vercel chạy HTTPS nên browser sẽ hỏi quyền **Local Network**; cần cho phép
để gọi node HTTP trong LAN. Firefox Desktop 153+ hỗ trợ quyền này mặc định;
Chrome/Edge mới cũng hỗ trợ. Client tự khai báo address space cho request
loopback/private-LAN. Browser cũ không hỗ trợ Local Network Access sẽ cần HTTPS
reverse proxy/tunnel trước node.

Người dùng chỉ nhập `Key` và `Value`. Browser tự tạo ví Ed25519 test-only, thêm
owner, lấy nonce, ký transaction và gửi JSON-RPC. Private key không rời browser.

Bulk client dùng cùng JSON-RPC:

```text
python tests/spam_transactions.py --config-dir config/template --via 9 --count 20000 --delay 0.005
```

`--via` phải là honest node. Script chỉ gửi transaction; proposer tự chọn theo
`MaxBlockTXs` và `MaxBlockBytes`.

## Grafana

Khi validator đang chạy:

```text
python tests/prepare_monitoring.py --config-dir config/template
docker compose -f logs/monitoring/docker-compose.yml up -d
```

Validator là process chạy trực tiếp trên máy, không nằm trong container. Script
đọc port HTTP thật từ `local-runs/<test>/node-*/admin`; Prometheus trong Docker
truy cập các process đó qua `host.docker.internal`.

Mở `http://127.0.0.1:3000`, đăng nhập `admin / lab01`. Dashboard hiển thị node
health, finalized-height agreement, mempool, consensus step/round, batch wait,
timeout/rejection, network fault, outbound queue và log của từng validator.

Nếu đổi testcase khi monitoring đang chạy, sinh lại target rồi restart Prometheus:

```text
python tests/prepare_monitoring.py --config-dir config/beo
docker compose -f logs/monitoring/docker-compose.yml restart prometheus
```

Nếu dashboard báo `No data`, kiểm tra `http://127.0.0.1:9090/targets`; các node
đang chạy phải có trạng thái `UP`.

```text
docker compose -f logs/monitoring/docker-compose.yml down
```

API debug trên port HTTP của mỗi node: `/status`, `/mempool`, `/ledger`,
`/metrics`. JSON-RPC nằm tại `POST /rpc`.

## Config node

Ví dụ `config/template/node-00.json`:

```json
{
  "NodeID": 0,
  "NetworkFile": "network.json",
  "OutputDir": "../../local-runs/template/node-00",
  "Behavior": "honest",
  "AdminAddress": "0.0.0.0:31000",
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
| `MaxBlockTXs` | `128` | Transaction tối đa mỗi block (trong `network.json`) |
| `MaxBlockBytes` | `4177920` | Kích thước body tối đa (trong `network.json`) |
| `Trace` | `false` | Log network chi tiết |
| `ConsoleLog` | `false` | In event log ra terminal |

Các tham số chung sau chỉ được load từ `network.json`:

| Field | Default | Ý nghĩa |
|---|---:|---|
| `MinBlockTXs` | `3` | Tạo block ngay khi đủ transaction thực thi được |
| `BlockMaxWaitMS` | `2000` | Nếu chưa đủ, tạo block sau thời gian chờ tối đa |
| `MempoolTTLMS` | `60000` | Xóa transaction chưa commit sau 1 phút |
| `AccountSlots` | `16` | Slot ưu tiên của mỗi account trước khi dùng queue |
| `AccountQueue` | `64` | Queue bổ sung tối đa của mỗi account |
| `GlobalSlots` | `1024` | Tổng slot chính của mempool |
| `GlobalQueue` | `256` | Tổng queue bổ sung dùng chung |

Behavior hỗ trợ: `invalid_signature`, `wrong_domain`, `duplicate_transaction`,
`replay_transaction`, `silent_proposer`, `crashed_proposer`, `equivocate`
(ký hai vote xung đột cho mỗi vote; chỉ dùng tối đa `f` node).

Network delay/drop/duplicate/reorder/replay nằm trong object `Faults`; xem
`src/realnet/faults.go` để biết các field.

Để tạo testcase khác, chạy lại với tên mới rồi chỉ sửa JSON:

```text
python tests/run.py --init delay-test --f 3
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

`run.py --node N` chạy riêng một node trong terminal hiện tại, phù hợp để cho
node khởi động trễ hoặc restart với `Recover: true` rồi quan sát tốc độ sync.

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
