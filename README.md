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

Node không tự tạo transaction hay block rỗng. Execution và consensus chạy trong
cùng process vì lab không có staking.

Yêu cầu: Python 3.9+ với `pip install cryptography`, Go theo `go.mod` (Go cục bộ
cũ hơn thì đặt `GOTOOLCHAIN=auto`), Docker nếu dùng Grafana.

## Chạy mạng

```text
python tests/run.py --init template --f 3          # tạo config/template
python tests/run.py --config-dir config/template   # chạy cả validator set
python tests/run.py --config-dir config/template --node 0   # hoặc từng node, mỗi node một terminal
python tests/run.py --help                         # mọi tùy chọn, gồm giới hạn mempool khi --init
```

- `config/<test>/`: `network.json`, `node-*.json`, key test.
- `local-runs/<test>/`: LevelDB, log, output. Xóa thoải mái; không dùng lại `OutputDir` cũ.
- Port: gRPC `29000..`, GossipSub `30000..`, HTTP (JSON-RPC, `/metrics`, ...) `31000..`.

Nếu một node bị kill, `run.py` in cảnh báo và để các node còn lại tiếp tục chạy.

## Chạy toàn bộ test

```text
python tests/run_tests.py --race --output local-runs/suite-1
```

Suite chạy lần lượt, dừng ở bước lỗi đầu tiên:

1. `gofmt` phải sạch.
2. `go test ./...` (có `--race` thì bật race detector).
3. `go vet ./...`.
4. `tests/test_vectors.py`: client Python ký ra đúng byte như Go.
5. Mọi file `tests/scenarios/t*.py` (T1–T8), mỗi file khởi động 10 process thật.

Mất khoảng 10 phút. `--output` phải là thư mục **chưa tồn tại**; bỏ qua thì tự
tạo `local-runs/suite-<thời gian>`. Thư mục này chứa `go-test.jsonl`,
`go-vet.log`, `<kịch bản>.log` và `summary.json` (PASS/FAIL từng phần).

Kịch bản dùng port cố định: không chạy hai suite/kịch bản cùng lúc và tắt mọi
cluster đang chạy tay trước.

Chạy lẻ:

```text
gofmt -l src && go vet ./... && go test ./... -count=1
python tests/test_vectors.py
python tests/scenarios/t1_normal.py
```

## Gửi transaction

**Web client** (`client/`, static, node không serve UI). Dùng bản đã deploy tại
<https://lab01-kappa.vercel.app/>, hoặc chạy cục bộ:

```bash
python3 -m http.server 8080 --directory client   # mở http://localhost:8080/
```

Nhập **Node URL** (ví dụ `http://127.0.0.1:31009`),
rồi chỉ cần nhập `Key` và `Value`. Browser tự tạo ví Ed25519 test-only, lấy nonce,
ký và gửi JSON-RPC; private key không rời browser. **Xem key-value state** hiển
thị state và nonce đã commit trên node đang chọn.

Deploy lên Vercel: `vercel --cwd client --prod` (hoặc Root Directory `client`,
preset **Other**, không build command). Để máy khác trong LAN gọi node, đặt
`"AdminAddress": "0.0.0.0:31000"`, nhập IP LAN thật (không phải `0.0.0.0`) vào
client, mở port trên firewall và cho phép quyền **Local Network** khi browser hỏi.

**Script client:** viết transaction trong `main()` của `tests/scripted_client.py`:

```text
NODE_URL=http://127.0.0.1:31009 python tests/scripted_client.py
```

Ví lưu ở `tests/wallets/<tên>.hex` (gitignore). Gửi tới node honest.

## Monitoring

API HTTP của mỗi node: `/status`, `/state`, `/mempool`, `/ledger`, `/metrics`;
JSON-RPC tại `POST /rpc` (state: `lab_state`).

Grafana, khi validator đang chạy:

```text
python tests/prepare_monitoring.py --config-dir config/template
docker compose -f logs/monitoring/docker-compose.yml up -d
```

Mở `http://127.0.0.1:3000` (`admin / lab01`). Đổi testcase thì chạy lại
`prepare_monitoring.py` rồi `docker compose ... restart prometheus`. Tắt bằng
`docker compose ... down`.

Khi panel báo `No data`:

- Metric: xem `http://127.0.0.1:9090/targets`, node phải `UP`.
- Log: Alloy đọc toàn bộ `local-runs/`; log trace rất lớn (T1 ~660 MB) nên lần
  đầu mất vài phút. Nếu vẫn thiếu, đọc lại từ đầu bằng
  `docker compose -f logs/monitoring/docker-compose.yml up -d --force-recreate loki alloy`,
  và xóa các `local-runs/<tên>` không cần.

Bảng **Validator events** lọc bằng biểu tượng phễu trên cột Node, Event, Height,
Round. Event tầng mạng (receive/send/gossip) và reject trùng lặp vô hại được ẩn.

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

| Field | Default | Nơi đặt | Ý nghĩa |
|---|---:|---|---|
| `Behavior` | `honest` | node | Hành vi validator |
| `SendGapMS` | `1` | node | Giới hạn outbound |
| `RetryMS` | `400` | node | Chu kỳ phát lại |
| `Trace` | `false` | node | Log network chi tiết |
| `ConsoleLog` | `false` | node | In event log ra terminal |
| `Recover` | `false` | node | Replay ledger khi khởi động (xem Full sync) |
| `BaseTimeoutMS` | `2000` | network | Timeout cơ sở, tăng theo round |
| `MaxBlockTXs` | `128` | network | Transaction tối đa mỗi block |
| `MaxBlockBytes` | `4177920` | network | Kích thước body tối đa |
| `MinBlockTXs` | `3` | network | Tạo block ngay khi đủ transaction thực thi được |
| `BlockMaxWaitMS` | `2000` | network | Chưa đủ thì tạo block sau thời gian này |
| `MempoolTTLMS` | `60000` | network | Xóa transaction chưa commit |
| `AccountSlots` / `AccountQueue` | `16` / `64` | network | Slot và queue của mỗi account |
| `GlobalSlots` / `GlobalQueue` | `1024` / `256` | network | Slot và queue toàn mempool |

Network delay/drop/duplicate/reorder/replay đặt trong object `Faults`; các field
xem `src/realnet/faults.go`.

Behavior (trong `src/runtime/adversary.go`); tối đa `f` node không honest,
`tests/run.py` từ chối config vi phạm:

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

## Kịch bản T1-T8

Mỗi file trong `tests/scenarios/` chạy không cần tham số và in `ok`/`FAIL` từng
kiểm tra, cuối cùng `RESULT <tên>: PASS|FAIL`. Phần chung do
`harness.scenario()` làm:

1. Tạo `config/<tên>` nếu chưa có (10 node, `f = 3`, `Trace` bật), chạy 10 process.
2. Gửi `count` transaction (mặc định 30, T1 dùng 300) vào node 9.
3. Chờ mọi honest node finalize đủ.
4. `check_agreement`: cùng chain, cùng state root, không `(sender, nonce)` nào áp dụng hai lần.
5. Gọi `checks(net)` riêng của kịch bản.

| File | Kịch bản | Kết quả bắt buộc |
|---|---|---|
| `t1_normal.py` | Không lỗi | Mọi honest node cùng chain và state root |
| `t2_duplicate_reorder.py` | Duplicate 100%, delay, reorder | Vote trùng bị từ chối, không finalize xung đột |
| `t3a_invalid_signature.py`, `t3b_wrong_domain.py` | Node 0 ký sai / sai domain | Message bị từ chối, lý do được log |
| `t4_replay_duplicate_tx.py` | Node 0 replay, node 1 duplicate transaction | Mỗi `(sender, nonce)` chỉ áp dụng một lần |
| `t5_drop_before_sync.py` | Drop 100% trong 8 giây đầu | Có drop, sau đó chain tiếp tục |
| `t6a_silent_proposer.py`, `t6b_crashed_proposer.py` | Proposer node 0 im lặng / thoát | Height 1 xong ở round >= 1 nhờ timeout |
| `t7_equivocation.py` | Node 0, 1, 2 ký hai vote xung đột | Honest node log `conflicting vote`, vẫn đồng thuận |
| `t8_same_seed.py` | Seed lỗi cố định, chạy hai lần | Cùng transaction đã finalize và cùng state root |

Output ở `local-runs/<tên>`, tự xóa trước mỗi lần chạy. Log hai lần chạy T8
không giống từng byte (có timestamp), nên T8 so transaction và state root.

Hàm trong `harness.py`:

| Hàm | Dùng để |
|---|---|
| `scenario(name, nodes=, network=, count=, timeout=, wait_nodes=, checks=)` | Chạy một lần, in `RESULT`, thoát |
| `run(...)` | Như `scenario` nhưng trả `(state_root, txs)` (T8) |
| `behaviors("equivocate", 0, 1, 2)` | Đặt `Behavior` cho các node |
| `faults(seed, DropBPS=..., ...)` | Cùng `Faults` cho mọi node (seed cộng NodeID) |
| `check(điều_kiện, mô_tả)` | Một dòng `ok`/`FAIL` |
| `net.count(event, nodes=)`, `net.reasons()`, `net.events(n)` | Đọc `events.jsonl` |
| `net.status(n)`, `net.ledger(n)`, `net.transactions(n)` | Đọc HTTP admin |
| `check_signature_rejected`, `check_timeout_recovered` | Kiểm tra dùng chung của T3, T6 |

`network=` (mặc định `{"BaseTimeoutMS": 1000}`) và `nodes=` chỉ có tác dụng khi
`config/<tên>` **chưa tồn tại**. Sau đó JSON trong thư mục là nguồn duy nhất;
muốn đổi thì sửa JSON hoặc xóa thư mục để sinh lại (key mới).

## Thêm kịch bản với behaviour mới

Ví dụ: behaviour `nil_voter` (luôn vote NIL) và kịch bản T9 kiểm tra `f` node như
vậy không chặn được chain. Mọi hành vi lỗi chỉ nằm trong
`src/runtime/adversary.go`; code honest chỉ gọi hook ở đó.

**1. Khai báo** hằng số và thêm vào `Valid()` (tên không có trong đó bị từ chối khi load config):

```go
NilVoter Behavior = "nil_voter" // T9: always vote NIL.

case "", Honest, InvalidSignature, /* ... */, Equivocate, NilVoter:
```

**2. Cài vào hook phù hợp:**

| Hook | Được gọi khi | Đang dùng bởi |
|---|---|---|
| `proposerTurn` | Tới lượt propose; trả `false` để bỏ qua | `silent_proposer`, `crashed_proposer` |
| `tamperProposal` | Trước khi phát proposal | `invalid_signature`, `wrong_domain` |
| `tamperVote` | Trước khi phát vote | `invalid_signature`, `wrong_domain` |
| `afterVote` | Sau khi phát vote | `equivocate` |
| `afterAdmit` | Transaction vừa vào mempool | `replay_transaction` |
| `afterFinalize` | Sau khi commit block | `replay_transaction` |
| `duplicateGossip` | Khi nhận/gửi transaction | `duplicate_transaction` |

Message bị sửa phải được **ký lại**, nếu không peer từ chối vì sai chữ ký thay vì
vì lý do cần kiểm tra. Ghi `behavior_message` để kịch bản chứng minh lỗi đã xảy ra:

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

Không hook nào phù hợp thì thêm method mới trong `adversary.go` (no-op khi
behaviour không khớp) và gọi nó ở đúng **một** chỗ trong `runtime.go`. Behaviour
làm process thoát có chủ ý thì cập nhật `allowed_exit` trong `tests/run.py`.

**3. Viết kịch bản** `tests/scenarios/t9_nil_voter.py`:

```python
"""T9: f = 3 validator luôn bỏ phiếu NIL. 2f+1 honest node vẫn finalize cùng một chain."""

from harness import behaviors, check, scenario

BYZANTINE = (0, 1, 2)


def checks(net):
    check(net.count("behavior_message", nodes=list(BYZANTINE)) > 0, "node Byzantine thật sự gửi vote NIL")


scenario("t9-nil-voter", nodes=behaviors("nil_voter", *BYZANTINE), checks=checks)
```

`check_agreement` luôn chạy trước, nên `checks` chỉ kiểm tra điều riêng. Kịch bản
chỉ có lỗi mạng thì không cần sửa Go: truyền `nodes=faults(...)` như T2/T5.

**4. Chạy và commit:** `gofmt -w src && go test ./... -count=1`, rồi
`python tests/scenarios/t9_nil_voter.py`. Commit `config/t9-nil-voter/` sinh ra ở
lần đầu để các lần sau dùng cùng key. `run_tests.py` tự nhận mọi `t*.py`. Muốn
event mới hiện trong log rút gọn (`Trace` tắt), thêm nó vào `importantEvents`
trong `src/realnet/eventlog.go`.

### Ví dụ 2: proposer chèn transaction trùng vào cuối block

Behaviour `duplicate_in_block`: khi propose, validator chép transaction cuối và
chèn vào cuối body. Khác `duplicate_transaction` (mempool chặn được), block này
được ký hợp lệ nên chỉ bước thực thi mới phát hiện.

Không sửa ở `block.Build` được vì hàm đó thực thi trước khi ký: danh sách trùng
sẽ lỗi ngay và không có gì được phát. Vì vậy sửa block hợp lệ trong
`tamperProposal`, rồi tính lại `BodyHash`, ký lại header và proposal. Thiếu bước
nào thì peer từ chối sớm vì lý do khác (`body commitment mismatch`, sai chữ ký).

```go
DuplicateInBlock Behavior = "duplicate_in_block" // T10: proposer appends a copy of its last transaction.

func (n *Runtime) tamperProposal(p *cons.Proposal) error {
	// Only fresh proposals: a re-proposal with proof would fail on the proof hash instead.
	if n.behavior == DuplicateInBlock && len(p.Proof) == 0 && len(p.Block.Body.Transactions) > 0 {
		return n.appendDuplicate(p)
	}
	// ... phần cũ giữ nguyên
}

// appendDuplicate keeps every signature valid so honest validators must execute
// the body to find the repeated nonce.
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

Diễn biến: honest node nhận header và body (chữ ký, `BodyHash` đều đúng), thực thi
và dừng ở bản sao với `transaction N: nonce K, expected K+1`, nên không ai prevote.
Round 0 hết hạn, node 1 propose block hợp lệ ở round 1.

`tests/scenarios/t10_duplicate_in_block.py`:

```python
"""T10: proposer node 0 chèn bản sao transaction cuối vào block; honest node phải từ chối."""

from harness import behaviors, check, check_timeout_recovered, scenario


def checks(net):
    check(net.count("behavior_message", nodes=[0]) > 0, "node 0 thật sự phát block có transaction trùng")
    rejected = {r: c for r, c in net.reasons().items() if ": nonce " in r and "expected" in r}
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

Dùng chuỗi `": nonce "` + `"expected"`, không dùng `"nonce"` đơn thuần: mempool
cũng log `replayed transaction nonce ...` và sẽ làm kiểm tra pass sai.

## Dữ liệu và full sync

Mỗi node lưu finalized block trong `ledger.db/` (LevelDB) và log trong
`events.jsonl`. Mempool chỉ nằm trong RAM; state được dựng lại từ block.

Đặt `"Recover": true` để node replay `ledger.db` khi khởi động: mỗi block `1..H`
được kiểm tra lại header, quorum PRECOMMIT, chữ ký transaction và `StateRoot`,
rồi node vào consensus ở height `H+1`. Block sai trên đĩa làm node dừng. Block
còn thiếu được kéo từ peer. Không bật `Recover` thì node từ chối chạy khi thư mục
output đã có dữ liệu. Dùng `run.py --node N` để restart riêng một node.

Giới hạn: vote/lock của height đang dở chưa được lưu, nên node vừa restart có thể
ký vote khác trước đó (tính như Byzantine fault). Chỉ restart tối đa `f` node cùng
lúc. Node bị kill cứng thì xóa `running.lock` trong thư mục output trước khi chạy lại.

## Canonical encoding

Byte của TX, HEADER, VOTE, BODY, PROPOSAL, TRANSPORT và state root được khóa bằng
golden vector `src/crypto/testdata/vectors.json`. Chỉ sinh lại khi cố ý đổi format
(mọi config/key cũ sẽ mất hiệu lực):

```text
go test ./src/crypto -run TestGoldenVectors -update
```

## Thư mục

| Thư mục | Trách nhiệm |
|---|---|
| `config` | Config testcase và test key |
| `local-runs` | Binary, ledger, log và output tạm |
| `src/crypto`, `src/types` | Ed25519, SHA-256, canonical encoding |
| `src/state`, `src/block` | Ownership, nonce, execution, Merkle root |
| `src/mempool`, `src/ledger` | RAM mempool và LevelDB ledger |
| `src/consensus` | Propose, PREVOTE, PRECOMMIT, finality |
| `src/runtime` | Node runtime độc lập IO; Byzantine behaviour trong `adversary.go` |
| `src/realnet` | GossipSub, gRPC stream, JSON-RPC/admin HTTP, network fault, event log |
| `tests` | Launcher `run.py`, suite `run_tests.py`, client Python, kịch bản T1-T8 |
| `logs` | Log runtime và bộ Grafana/Prometheus/Loki/Alloy |
