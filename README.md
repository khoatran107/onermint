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
Nút **Xem key-value state** hiển thị committed key-value và committed nonce của
từng client trên node đang chọn; đổi Node URL để so sánh các validator.

Client dạng script dùng cùng JSON-RPC: viết từng transaction trong `main()` của
`tests/scripted_client.py` rồi chạy (cần `pip install cryptography`):

```text
python tests/scripted_client.py
NODE_URL=http://127.0.0.1:31009 python tests/scripted_client.py
```

Mỗi ví (`wallet("alice")`) được lưu ở `tests/wallets/<tên>.hex` (test-only, đã
gitignore). Node nhận phải là honest node. Script chỉ gửi transaction; proposer
tự chọn theo `MaxBlockTXs` và `MaxBlockBytes`.

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

Các panel đọc log (Consensus step, Proposer per height and round, Validator
events) hỗ trợ cả hai format của `events.jsonl`: log rút gọn (`Trace: false`) và
log trace (`Trace: true`, mọi config T1–T8 đều bật). Nếu các panel này thiếu dữ
liệu của lần chạy gần nhất:

- Alloy đọc toàn bộ `local-runs/`. Log trace rất lớn (riêng T1 khoảng 660 MB),
  nên lần đọc đầu cần vài phút. Theo dõi tiến độ ở
  `http://127.0.0.1:12345/metrics`: `loki_source_file_read_bytes_total` phải
  tiến dần tới `loki_source_file_file_bytes_total`.
- Nếu Alloy bị dừng trong lúc một kịch bản xóa rồi tạo lại `local-runs/<tên>`,
  vị trí đọc cũ có thể không còn đúng. Tạo lại Loki và Alloy để đọc lại mọi log
  từ đầu (dữ liệu Loki chỉ là bản sao của các file `events.jsonl`):

```text
docker compose -f logs/monitoring/docker-compose.yml up -d --force-recreate loki alloy
```

- Xóa các thư mục `local-runs/<tên>` không còn cần để giảm lượng log phải đọc.

```text
docker compose -f logs/monitoring/docker-compose.yml down
```

API debug trên port HTTP của mỗi node: `/status`, `/state`, `/mempool`,
`/ledger`, `/metrics`. JSON-RPC nằm tại `POST /rpc`; state dùng method
`lab_state`.

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

Behavior hỗ trợ (định nghĩa trong `src/runtime/adversary.go`):

| Behavior | Hành vi | Dùng trong |
|---|---|---|
| `honest` | Mặc định | — |
| `invalid_signature` | Lật một byte chữ ký của proposal/vote | T3a |
| `wrong_domain` | Ký lại với domain context bị sửa | T3b |
| `duplicate_transaction` | Gossip lại mỗi transaction nhận được | T4 |
| `replay_transaction` | Gossip lại transaction cũ sau mỗi block | T4 |
| `silent_proposer` | Không bao giờ propose | T6a |
| `crashed_proposer` | Thoát (exit code 86) khi tới lượt propose | T6b |
| `equivocate` | Ký thêm một vote xung đột cho mỗi vote | T7 |

Tổng số node không honest không được vượt quá `f`; `tests/run.py` từ chối
config vi phạm.

Network delay/drop/duplicate/reorder/replay nằm trong object `Faults`; xem
`src/realnet/faults.go` để biết các field.

Để tạo testcase khác, chạy lại với tên mới rồi chỉ sửa JSON:

```text
python tests/run.py --init delay-test --f 3
```

Không dùng lại `OutputDir` cũ. Xóa `local-runs/<test>` nếu muốn bỏ output;
config testcase vẫn còn trong `config/<test>`.

## Kịch bản T1-T8 trên mạng thật

Mỗi kịch bản là một file Python ngắn trong `tests/scenarios/`, chạy không cần
tham số. File chỉ khai báo cấu hình lỗi và các kiểm tra riêng; phần chung do
`harness.scenario()` làm:

1. Tạo `config/<tên>` nếu chưa có (10 node, `f = 3`), rồi chạy 10 process.
2. Gửi `count` transaction (mặc định 30, T1 dùng 300) vào node 9.
3. Chờ mọi honest node finalize đủ transaction.
4. `check_agreement`: cùng chain, cùng state root, không `(sender, nonce)` nào
   bị áp dụng hai lần.
5. Gọi `checks(net)` của kịch bản, đọc trạng thái cuối qua HTTP admin
   (`/status`, `/ledger`) và diễn biến ở giữa qua `events.jsonl`.

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
nếu Go cục bộ cũ hơn `go.mod`, đặt `GOTOOLCHAIN=auto`.
Log không giống từng byte giữa hai lần chạy T8 vì có timestamp, nên T8 so sánh
theo transaction đã finalize và state root.

Các hàm trong `harness.py` dùng khi viết kịch bản:

| Hàm | Dùng để |
|---|---|
| `scenario(name, nodes=, network=, count=, timeout=, wait_nodes=, checks=)` | Chạy một lần rồi in `RESULT` và thoát |
| `run(...)` | Như `scenario` nhưng trả về `(state_root, txs)` để so sánh nhiều lần chạy (T8) |
| `behaviors("equivocate", 0, 1, 2)` | Đặt cùng một `Behavior` cho các node |
| `faults(seed, DropBPS=..., ...)` | Cùng một `Faults` cho mọi node (seed cộng thêm NodeID) |
| `check(điều_kiện, mô_tả)` | Một dòng `ok`/`FAIL` |
| `net.count(event, nodes=)`, `net.reasons()`, `net.events(n)` | Đọc `events.jsonl` |
| `net.status(n)`, `net.ledger(n)`, `net.transactions(n)` | Đọc HTTP admin |
| `check_signature_rejected`, `check_timeout_recovered` | Kiểm tra dùng chung của T3 và T6 |

`network=` mặc định là `{"BaseTimeoutMS": 1000}`. Tham số `network` và `nodes`
chỉ được dùng khi `config/<tên>` **chưa tồn tại**; sau đó config trong thư mục
là nguồn duy nhất. Muốn đổi, sửa JSON hoặc xóa `config/<tên>` để sinh lại (khi
đó key mới được tạo).

## Thêm kịch bản với behaviour mới

Ví dụ: thêm Byzantine behaviour `nil_voter` (luôn bỏ phiếu NIL) và kịch bản T9
kiểm tra rằng `f` node như vậy không chặn được chain. Mọi hành vi lỗi chỉ nằm
trong `src/runtime/adversary.go`; runtime honest chỉ gọi các hook ở đó.

**1. Khai báo behaviour** trong `src/runtime/adversary.go`: thêm hằng số và đưa
vào `Valid()` (node dùng hàm này khi load config; tên không có trong đó bị từ chối):

```go
NilVoter Behavior = "nil_voter" // T9: always vote NIL.

case "", Honest, InvalidSignature, /* ... */, Equivocate, NilVoter:
```

**2. Cài hành vi vào hook phù hợp.** Các hook có sẵn và chỗ runtime gọi chúng:

| Hook | Được gọi khi | Ví dụ đang dùng |
|---|---|---|
| `proposerTurn` | Tới lượt node propose; trả `false` để bỏ qua | `silent_proposer`, `crashed_proposer` |
| `tamperProposal` | Trước khi phát proposal | `invalid_signature`, `wrong_domain` |
| `tamperVote` | Trước khi phát mỗi vote của node | `invalid_signature`, `wrong_domain` |
| `afterVote` | Sau khi đã phát vote | `equivocate` |
| `afterAdmit` | Transaction vừa vào mempool | `replay_transaction` |
| `afterFinalize` | Sau khi node commit một block | `replay_transaction` |
| `duplicateGossip` | Khi nhận/gửi transaction | `duplicate_transaction` |

`nil_voter` sửa vote trước khi phát nên dùng `tamperVote`. Vote phải được ký
lại, nếu không peer sẽ từ chối vì sai chữ ký thay vì đếm một vote NIL hợp lệ.
Ghi event `behavior_message` để kịch bản kiểm tra được rằng lỗi thật sự xảy ra:

```go
func (n *Runtime) tamperVote(v *types.Vote) error {
	if n.behavior == NilVoter && !v.IsNil {
		v.IsNil, v.BlockHash = true, types.Hash{}
		signed, err := crypt.SignVote(*v, n.key)
		if err != nil {
			return err
		}
		*v = signed
		n.record(network.Event{Event: "behavior_message", Height: v.Height, Round: v.Round, Phase: uint8(v.Phase), Reason: string(n.behavior)})
		return nil
	}
	// ... phần cũ giữ nguyên
}
```

Nếu không hook nào phù hợp, thêm một method mới vào `adversary.go` (no-op khi
`n.behavior` không khớp) và gọi nó đúng **một** chỗ trong `runtime.go`. Nếu
behaviour làm process thoát có chủ ý như `crashed_proposer`, cập nhật
`allowed_exit` trong `tests/run.py`.

**3. Viết kịch bản** `tests/scenarios/t9_nil_voter.py`:

```python
"""T9: f = 3 validator luôn bỏ phiếu NIL. 2f+1 honest node vẫn finalize cùng một chain."""

from harness import behaviors, check, scenario

BYZANTINE = (0, 1, 2)


def checks(net):
    check(net.count("behavior_message", nodes=list(BYZANTINE)) > 0, "node Byzantine thật sự gửi vote NIL")


scenario("t9-nil-voter", nodes=behaviors("nil_voter", *BYZANTINE), checks=checks)
```

`check_agreement` (cùng chain, cùng root, không áp dụng trùng) luôn chạy trước
`checks`, nên `checks` chỉ cần kiểm tra điều riêng của kịch bản. Kịch bản lỗi
mạng không cần Go: chỉ truyền `nodes=faults(...)` như T2/T5.

**4. Chạy và commit.**

```text
gofmt -w src
go test ./... -count=1
python tests/scenarios/t9_nil_voter.py
```

Lần chạy đầu sinh `config/t9-nil-voter/`; commit thư mục đó để lần sau chạy lại
cùng key và cùng transaction. `tests/run_tests.py` tự chạy mọi file
`tests/scenarios/t*.py`, nên không cần đăng ký thêm. Nếu muốn event mới hiện
trong log rút gọn (khi `Trace` tắt) và Grafana, thêm tên event vào
`importantEvents` trong `src/realnet/eventlog.go`; kịch bản luôn bật `Trace`
nên không bị ảnh hưởng.

### Ví dụ 2: proposer chèn transaction trùng vào cuối block

Behaviour `duplicate_in_block`: khi tới lượt propose, validator lấy transaction
cuối của block, chèn thêm một bản sao vào cuối body rồi mới phát. Đây là lỗi
"đề xuất block không hợp lệ" của mô hình Byzantine. Khác với
`duplicate_transaction` (chỉ gossip trùng, mempool chặn được), lần này block
mang transaction trùng và được ký đầy đủ, nên chỉ bước thực thi khi validate
block mới phát hiện được.

**Vì sao không sửa ở mempool hay `block.Build`?** `block.Build` luôn thực thi
transaction trước khi ký. Một danh sách có transaction trùng sẽ lỗi nonce ngay
tại đó và node chỉ log `proposal_failed`, không có gì được phát ra. Vì vậy
behaviour cho engine tạo block hợp lệ như bình thường, rồi sửa nó ở hook
`tamperProposal`, ngay trước khi phát ra mạng.

**Muốn peer thực sự phải thực thi block thì mọi chữ ký phải đúng.** Sau khi chèn,
proposer tính lại `BodyHash`, ký lại header và ký lại proposal. Nếu bỏ một bước,
peer sẽ từ chối sớm vì lý do khác (`body commitment mismatch` hoặc sai chữ ký),
và kịch bản không còn kiểm tra đúng điều cần kiểm tra. `StateRoot` giữ nguyên;
điều đó không quan trọng vì thực thi đã thất bại trước bước so root.

Trong `src/runtime/adversary.go` (thêm hằng số vào `Valid()` như bước 1):

```go
DuplicateInBlock Behavior = "duplicate_in_block" // T10: proposer appends a copy of its last transaction.

func (n *Runtime) tamperProposal(p *cons.Proposal) error {
	// Chỉ sửa proposal mới; proposal lặp lại kèm proof sẽ bị từ chối vì proof sai hash.
	if n.behavior == DuplicateInBlock && len(p.Proof) == 0 && len(p.Block.Body.Transactions) > 0 {
		return n.appendDuplicate(p)
	}
	// ... phần cũ giữ nguyên
}

// appendDuplicate appends a copy of the block's last transaction, then
// re-commits and re-signs body hash, header and proposal. The result is
// authentic (every signature verifies) but invalid: executing the body fails
// on the repeated nonce, so honest validators must reject it.
func (n *Runtime) appendDuplicate(p *cons.Proposal) error {
	txs := p.Block.Body.Transactions
	p.Block.Body.Transactions = append(txs[:len(txs):len(txs)], txs[len(txs)-1].Clone())
	bodyHash, err := crypt.BodyHash(n.chain, p.Block.Body)
	if err != nil {
		return err
	}
	header := p.Block.Header
	header.BodyHash = bodyHash
	if p.Block.Header, err = crypt.SignHeader(header, n.key); err != nil {
		return err
	}
	signed, err := cons.SignProposal(*p, n.key)
	if err != nil {
		return err
	}
	*p = signed
	n.record(network.Event{Event: "behavior_message", Height: p.Height, Round: p.Round, Reason: string(n.behavior)})
	return nil
}
```

Diễn biến mong đợi:

1. Header và chữ ký proposal hợp lệ, nên honest node chấp nhận header và yêu
   cầu body.
2. `BodyHash` khớp, nên body được nhận.
3. Engine thực thi body và dừng ở bản sao với lỗi
   `transaction N: nonce K, expected K+1`. Block bị từ chối và lý do được log.
4. Không ai prevote cho block đó. Round 0 hết hạn, proposer kế tiếp (node 1) đề
   xuất block hợp lệ ở round 1.

Node 0 tự prevote cho bản block *gốc* mà engine của nó đã tạo. Không peer nào có
bản đó, nên vote này không bao giờ đủ quorum.

`tests/scenarios/t10_duplicate_in_block.py`:

```python
"""T10: proposer node 0 chèn bản sao transaction cuối vào cuối block của nó.

Block vẫn có chữ ký hợp lệ nhưng thực thi thất bại ở nonce lặp lại, nên mọi
honest node phải từ chối nó; height đó xong ở round sau và không transaction
nào bị áp dụng hai lần.
"""

from harness import behaviors, check, check_timeout_recovered, scenario


def checks(net):
    check(net.count("behavior_message", nodes=[0]) > 0, "node 0 thật sự phát block có transaction trùng")
    reasons = net.reasons()
    rejected = {r: c for r, c in reasons.items() if ": nonce " in r and "expected" in r}
    check(bool(rejected), f"honest node từ chối block vì nonce lặp lại: {rejected}")
    check_timeout_recovered(net)


scenario("t10-duplicate-in-block", nodes=behaviors("duplicate_in_block", 0), checks=checks)
```

Kết quả khi chạy thử:

```text
  ok   9 honest nodes hold byte-identical chains (height 1)
  ok   all honest nodes report the same state root
  ok   no (sender, nonce) is applied twice
  ok   all 30 spam transactions finalized once (found 30)
  ok   node 0 thật sự phát block có transaction trùng
  ok   honest node từ chối block vì nonce lặp lại: {'transaction 3: nonce 3, expected 4': 90}
  ok   height 1 could only finish after a timeout (decided in round 1)
RESULT t10-duplicate-in-block: PASS
```

Kiểm tra lý do từ chối bằng chuỗi `": nonce "` + `"expected"`, không dùng
`"nonce"` đơn thuần. Mempool cũng log `replayed transaction nonce ...` khi
transaction đã commit được gossip lại, và chuỗi đó sẽ làm kiểm tra pass sai.

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

Canonical encoding (TX, HEADER, VOTE, BODY, PROPOSAL, TRANSPORT, state root)
được khóa bằng golden vector trong `src/crypto/testdata/vectors.json`.
`tests/test_vectors.py` kiểm tra client Python ký ra đúng byte như Go. Chỉ sinh
lại vector khi cố ý đổi format (khi đó mọi config/key cũ không còn hợp lệ):

```text
go test ./src/crypto -run TestGoldenVectors -update
```

Integration suite (Go test, vet, golden vector của client Python, rồi lần lượt
mọi file trong `tests/scenarios/`; `--race` chỉ áp dụng cho `go test`):

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
| `src/consensus` | Propose, PREVOTE, PRECOMMIT, finality (`engine.go`, `proposal.go`, `decision.go`, `votes.go`) |
| `src/runtime` | Node runtime độc lập IO; Byzantine behaviour trong `adversary.go` |
| `src/realnet` | GossipSub, gRPC stream, JSON-RPC/admin HTTP, network fault, event log |
| `tests` | Launcher `run.py`, suite `run_tests.py`, client Python, kịch bản T1-T8 |
| `logs` | Log runtime và bộ Grafana/Prometheus/Loki/Alloy |
