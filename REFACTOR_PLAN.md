# Refactor plan — Lab01 blockchain

Scope: `src/` (Go) and `tests/` (Python harness + scenarios). JavaScript clients are out of scope.
Goal: remove duplicated logic, split oversized files/functions, and centralise constants,
**without changing observable behaviour**.

Snapshot reviewed: branch `refactor` @ `ac12923`.

## Status (executed)

Phases 0–5 are done, plus the hygiene items in §6 that don't rename packages. Every phase was checked
with `go vet`, `go test -race`, the golden byte vectors and the T1–T8 suite. Deliberately **not** done:

- **§2.5 "make `Persist` required":** tests rely on the in-memory ledger mode, and the change brings little benefit.
- **§4.7 sharing `/status` with `lab_status`:** the two return different JSON shapes, which the harness and UI read. Merging them would change the API.
- **§6 package renames (`runtime`→`node`, `network`→`wire`, the `crypt` alias):** churn across every import for naming only. These are left for the team to decide.
- **§7 issues:** these are behaviour changes, not refactors. They are untouched except #5: `reject_transport` now goes through
  `p.Record`, so it is counted in `/metrics`.

---

## 0. Ground rules (read first)

These are the things a refactor here can silently break. Every phase below must keep them fixed.

| Invariant | Why it matters | Who depends on it |
|---|---|---|
| Canonical byte encodings (`TX`, `HEADER`, `VOTE`, `BODY`, `PROPOSAL`, `TRANSPORT`, `STATE-MERKLE`) | Signatures and hashes. Changing one byte invalidates every committed key/config and breaks the clients. | Go nodes, `tests/client.py`, `client/app.js` |
| JSON shape of `types.*`, `consensus.Proposal`, `consensus.Finalized` | Stored in LevelDB, sent on the wire, read by `/ledger` and the Python harness (`f["Block"]["Header"]`, `f["Round"]`). | ledger, harness, UI |
| Event names and fields in `events.jsonl` (`consensus_vote_signed`, `fault_drop`, `behavior_message`, `state_commit`, `Detail`/`Reason`, …) | Scenario checks count them; the Grafana dashboard queries them. | `tests/scenarios/*.py`, `logs/monitoring/grafana/dashboards/lab01.json` |
| Behaviour names (`invalid_signature`, `crashed_proposer`, …) and exit code `86` | Written in committed `config/*/node-*.json`, and checked by `tests/run.py`. | configs, launcher |
| Deterministic iteration order (validator-list order, sorted rounds) | Spec §8 determinism; certificate/proof order is signed. | consensus |
| Submission layout (`src/ tests/ logs/ config/ README.md REPORT.pdf`) | Spec §10: a wrong layout scores 0. | — |

> This plan file is not part of the submission. Delete it or move it out of the repo before zipping.

### Phase 0: safety net (do this before touching code)

1. **Golden byte vectors.** Add `src/crypto/testdata/vectors.json` with a fixed key, then each canonical
   payload (tx, header, vote, body, proposal, transport envelope, state root) and its expected hex.
   - A Go test asserts every encoder reproduces the vectors.
   - A Python test asserts `tests/client.py`'s `canonical_tx_bytes` matches the tx vector.
     Today, keeping Go and Python in sync relies only on a comment ("Must stay byte-identical…").
2. **Baseline run.** Run `python tests/run_tests.py --output local-runs/baseline` and keep `summary.json`.
   Every phase must end with the same Go test count passing and T1–T8 at PASS.
3. Run `go test ./... -race` once to catch hidden data races before the code starts moving.

---

## 1. Shared primitives (`types`, `crypto`): remove cross-package copy-paste

Small, low-risk changes. Every later phase depends on them.

### 1.1 Deep-copy helpers (duplicated 6×)
The same "copy the tx and re-slice `Value`" loop appears in:
- `consensus/messages.go:104` `cloneBlock`
- `block/block.go:65-69` (body copy in `Build`)
- `mempool/pool.go:631, 665, 722` (Admit / Select / Snapshot)
- `runtime/runtime.go:520, 588` (`copyTX` twice)
- `consensus/messages.go:113` `cloneProposal`, `engine.go:182` (proof slice copy)

**Do:** add `func (t Transaction) Clone() Transaction`, `func (b BlockBody) Clone()`, and `func (b Block) Clone()`
to `src/types/types.go`. Keep `cloneProposal` in consensus, built on `Block.Clone()`.

### 1.2 Chain-ID validation (duplicated 7×)
`c == "" || !utf8.ValidString(c)` is repeated in `consensus.New`, `CanonicalProposalBytes`,
`block.Context.validate`, `state.Execute`, `state.Root`, `realnet.TransportBytes`, and
(partly) `crypto.newEncoder`.

**Do:** add `crypto.ValidateChainID(string) error` and call it everywhere.

### 1.3 Private-key/identity check (duplicated 3×)
`ed25519.NewKeyFromSeed(key[:SeedSize])` + `bytes.Equal` + public-key comparison appears in
`crypto.sign` (`crypto.go:47`), `consensus.SignProposal` (`messages.go:78`), and `consensus.New`
(`engine.go:137`).

**Do:** export `crypto.CheckKey(key ed25519.PrivateKey, pub types.PublicKey) error`, and
`crypto.IdentityOf(key) (types.ValidatorID, error)` for `consensus.New`.

### 1.4 One canonical encoder (3 hand-rolled copies)
`crypto/encoding.go` has a private `encoder` (version byte + `u32`-length-prefixed `DOMAIN:chain` + …).
Two other files rebuild the same layout by hand:
- `consensus/messages.go:38` `CanonicalProposalBytes`: same prefix, `u64`s, `u32` count, blobs.
- `realnet/auth.go:32` `TransportBytes`: same prefix, plus `u32` sender/recipient.

The byte layouts match the encoder exactly, so this is a pure refactor.

**Do:**
- Export `crypto.Encoder` (`NewEncoder(domain, chain)`, `U32`, `U64`, `Byte`, `Fixed([]byte)`, `Blob`, `Str`, `Bytes() ([]byte, error)`).
- Rewrite both functions on top of it, and verify them against the Phase-0 vectors.
- Put the domain strings in one place: `const DomainTx = "TX"`, `DomainHeader`, `DomainVote`, `DomainBody`, `DomainProposal`, `DomainTransport`, `DomainStateMerkle`.
- Consider moving `Sign/VerifyProposal` into `crypto` next to `Sign/VerifyVote` so every signing domain lives in one package.
  Alternatively, keep them in consensus but use the shared `sign/verify` helpers instead of calling `ed25519` directly.

### 1.5 Deterministic sort-by-(sender, nonce) (duplicated 2×, similar 2×)
`mempool/pool.go:647` and `:713` contain the same `sort.Slice` closure. `state.go:408` and
`commitment.go:514` both sort public keys with `bytes.Compare`.
**Do:** `pool.sortedEntries()` in mempool, and `types.ComparePublicKey(a, b)` (or a `sortedSenders()` in state).

---

## 2. Consensus engine (`src/consensus`)

`engine.go` is 647 lines, and most of the duplication is in the validation paths.

### 2.1 `blockContext(originRound)`: 5 identical literals
`block.Context{ChainID: e.chain, Height: e.height, ParentHash: e.parent, Proposer: e.Proposer(e.height, X)}`
appears at `engine.go:270, 328, 372, 580, 605, 622`.
**Do:** `func (e *Engine) blockContext(origin uint64) block.Context`.

### 2.2 One quorum-certificate verifier (2 near-identical loops)
`checkProof` (`engine.go:298-313`) and `ValidateDecisionHeader` (`engine.go:630-645`) both:
check membership and uniqueness, match the vote scope (height/round/phase/non-nil/hash), verify the signature, and count toward the quorum.
They differ only in phase (Prevote vs Precommit) and in the error text.
**Do:**
```go
func (e *Engine) verifyCertificate(votes []types.Vote, round uint64, phase types.VotePhase, hash types.Hash) error
```
Keep the existing error strings: the scenario checks match on substrings such as "signature".

### 2.3 Decision ingestion: `Restore` and `ReceiveFinalized` share a body
Both run `ValidateDecisionHeader` → `block.Validate` → `crypt.BlockHash` (`engine.go:577-587` vs `602-612`).
They differ only in what happens afterwards (commit without persisting vs `finalize`).
**Do:** `func (e *Engine) verifyDecision(f Finalized) (candidate, types.Hash, error)`, then
`Restore` = verify + `commit(c, hash)`, and `ReceiveFinalized` = verify + `finalize(...)`.
Extract `commit()` (set `committed`, `parent`, `height++`) out of `finalize` as well.

### 2.4 Redundant work in the proposal path
`ReceiveProposal` calls `ValidateProposalHeader`, which already computes `BlockHash`, runs `ValidateHeader` and
`checkProof` (returning the proof round, which is discarded). It then:
- re-hashes the header (`engine.go:321`),
- re-verifies the header signature through `block.Validate` → `ValidateHeader`,
- re-derives `proofRound` from `p.Proof[0].Round` (`:332-335`).

At runtime, `ValidateProposalHeader` also runs on header receipt (`runtime.go:407`) and then again inside
`ReceiveProposal`. Every proposal is therefore checked twice, with up to 7 vote-signature verifications each time.

**Do:** make an internal `validateProposalHeader(p) (hash, proofRound, error)` that returns what it computed, and
have `ReceiveProposal` reuse it. Add a `block.ValidateBody(ctx, state, block)` that skips the header re-check
when the caller has already validated the header. The same applies to decisions: `ValidateDecisionHeader` runs at header
time and again in `ReceiveFinalized`.

### 2.5 Smaller cleanups
- `Timeout` (`engine.go:226-238`): the `Propose` and `Prevote` cases are identical except for the step and phase. Use a
  `nextStep`/`phaseOf` table, or a helper `e.voteNilAndAdvance(step, phase)`.
- `progress()` (`:504-514`) calls `e.certificate(r, phase, v)` 2–3 times per choice. Compute it once per iteration.
- `finalize` (`:545, 553`) builds the same `Finalized` value twice. Build it once and clone it for `out`.
- Event field patching (`e.out.Events[len(e.out.Events)-1].Phase = …`, `:406, 439-441`) is fragile.
  Pass the overrides in instead: `e.emitEvent(Event{Kind: "vote_received", Validator: …, Phase: …, Round: …})`, filling
  defaults for zero fields, or a small `eventOpt` func list.
- Lock/valid state: group `locked/lockedHash/lockedRound` and `valid/validHash/validRound/validProof` into
  `type roundValue struct{ set bool; hash types.Hash; round uint64; proof []types.Vote }`. Then `resetHeight`
  becomes `e.lock, e.valid = roundValue{}, roundValue{}`, and `observeQuorum` is two calls to `rv.raise(...)`.
- `Step.String()` should live in consensus. Today it is `realnet/admin.go:334 consensusStepName`.
- `Engine.ledger` is only filled when `Persist == nil` (a test-only mode). Consider making `Persist` required
  and giving tests an in-memory persister. That removes the dual path in `finalize`.
- File split: `engine.go` (state machine, timeouts, votes), `proposal.go` (Make/Receive/ValidateProposal*,
  proof checks), `decision.go` (finalize, Restore, ReceiveFinalized, ValidateDecisionHeader). Merge `order.go`
  into the vote-tallying file.

---

## 3. Node runtime (`src/runtime/runtime.go`)

### 3.1 Extract Byzantine behaviours from the honest path (largest win)
`Behavior` string checks are spread across `process()`, `Receive()`, `admit()`, `Submit()`
(`runtime.go:189, 194, 219-249, 259, 302, 382, 519, 594`). The honest logic is hard to read because every
send has an `if n.behavior == …` around it.

**Do:**
1. Make it typed: `type Behavior string` with constants in **one** place, plus `Behavior.Valid()`. Today the list is
   duplicated in `realnet/config.go:153`, and the literals are scattered across runtime, Python (`run.py:182`,
   scenarios), and `main.go`. The JSON values do not change.
2. Add a hook interface with an honest no-op default:
   ```go
   type adversary interface {
       beforePropose(s cons.Status) (skip bool, err error) // silent / crashed proposer
       outgoingProposal(*cons.Proposal) error               // invalid_signature / wrong_domain
       outgoingVote(*types.Vote) (extra []types.Vote, err error) // + equivocate
       afterAdmit(tx) / afterFinalize() / onSubmit(tx)     // duplicate / replay tx
   }
   ```
   Put the implementations in `runtime/adversary.go`. The honest `Runtime` never mentions a behaviour name.
3. The `invalid_signature` and `wrong_domain` mutations for proposals and votes (`:219-228` vs `:237-247`) are
   the same "flip a byte, then re-sign" logic. Write one `corrupt(raw []byte, sig *types.Signature)` helper.
4. Also move `ErrCrash` and the magic exit code `86` (`main.go:546`, `run.py:128,182`) into a named constant.

### 3.2 Split `process()` (125 lines, 6 responsibilities)
```go
func (n *Runtime) process() error {
    if err := n.maybePropose(); err != nil { return err }
    o := n.engine.Drain()
    n.recordEngineEvents(o.Events)
    // scheduleTimers / publishProposals / publishVotes / onFinalized
}
```
`onFinalized(f)` (`:265-307`) should itself call `pruneAfterHeight(h)`, which covers the pool, `localTX`, `remote`, `ads`, and `votes`.

### 3.3 Repeated send/record idioms
- Vote broadcasting (`json.Marshal(v)` → `broadcast(Message{Kind: Vote, …})`) is written out 3× (`:252-257, 178-183,
  349-355`). TX gossip already has `gossipTX`, so add a matching `broadcastVote(v)`.
- `n.port.Record(network.Event{Node: n.id, Peer: -1, …})` is written out 12×. Add `n.record(event string, fields…)` or
  `n.event(network.Event)`, which fills `Node`/`Peer`.
- `if errors.Is(err, cons.ErrPersistence) { return err }; n.port.Record(reject…)` is written out 3× (`:197, 479, 494`).
  Add `n.engineResult(err, rejectEvent) error`.
- `errors.Is(err, network.ErrQueueFull) → nil` appears in `send` and again in the Header case (`:422`). Add `ignoreQueueFull(err)`.

### 3.4 `wireHeader` behaviour as methods
`Receive` branches on `h.Proposal != nil` vs `h.Decision != nil` in the Header **and** Body cases (`:400-416, 454-477`).
Move that into methods:
```go
func (h wireHeader) scope() (height, round uint64)
func (h wireHeader) header() types.BlockHeader
func (h wireHeader) hasBody() bool
func (h wireHeader) validate(e *cons.Engine) error
func (h wireHeader) deliver(e *cons.Engine, body types.BlockBody) error
```
Then `onHeader` and `onBody` become short, linear functions. Each `Receive` case becomes `n.onTx/onHeader/onRequest/onBody/onVote`.

`realnet/adversary_test.go:19 attackHeader` is a field-for-field copy of `wireHeader`. Export `runtime.WireHeader`
(or move it to `network`) and reuse it.

### 3.5 Config plumbing
`runtime.Config` has 22 flat fields, which `realnet/run.go:456` copies one by one from `realnet.Config`.
**Do:** define grouped structs once (`MempoolLimits{TTL, AccountSlots, AccountQueue, GlobalSlots, GlobalQueue, Bytes, MaxTxBytes}`,
`BlockLimits{MinTXs, MaxTXs, MaxBytes, MaxWait}`) and embed them in both configs. The JSON stays flat if they are
embedded anonymously, so existing `network.json`/`node-*.json` remain valid.
`mempool.New(6 positional ints)` should take `MempoolLimits` too, because positional ints are easy to swap.

---

## 4. Real network process (`src/realnet`)

### 4.1 Split `run.go` (693 lines)
| New file | Contents moved from `run.go` |
|---|---|
| `eventlog.go` | `logWriter`, `importantEvent`, the filter and rate-limit policy |
| `scheduler.go` | `task`, `tasks` heap, `Schedule`, the "pop due task / wait" part of the loop |
| `outbound.go` | `Send`, `frameEvent`, `sender`, `transmit` |
| `inbound.go` | `acceptFrame`, `enqueueVerified`, `receive` (peer rate limiting/blocking), `AcceptHeader` |
| `run.go` | `Run`, `runWithNode` (setup/teardown), the owner loop `select` |

Inside `runWithNode`, extract `openOutputs(c) (lock, file, store, cleanup)` and `startServers(p)`. The function
currently mixes about 8 deferred cleanups, which makes errors on the shutdown path hard to follow.

### 4.2 One owner-loop request helper (3 copies of the double `select`)
`enqueueVerified` (`run.go:305`), `submitTransaction` (`submit.go:542`) and `snapshot` (`admin.go:92`) all do
"send on `p.in` honouring ctx and p.ctx, then wait for the reply honouring ctx and p.ctx".
**Do:** a generic `func call[T any](p *process, ctx context.Context, r received, reply <-chan T) (T, error)`.
Also move the snapshot handling (`run.go:642-662`, with its inline anonymous interface type assertion) into
`p.answerQuery(q)`, and declare the inspector interface as a named type next to `localNode`.

### 4.3 Envelope helpers
- Signing an envelope (`TransportBytes` → `ed25519.Sign`) is duplicated in `Send` (`run.go:237-241`) and `hello`
  (`stream.go:248-252`). Add `p.signEnvelope(e)`.
- The broadcast recipient `^uint32(0)` is a magic value at `run.go:280, 396`, `auth.go:271`, and `gossip.go:109`. Add `const broadcastRecipient`.
- The kinds that may be broadcast are listed in `run.go:218` and again in `auth.go:272`. The valid kinds are listed as raw
  strings at `auth.go:237` (which also includes `"hello"`, absent from `network.Kind`). Add `network.Hello` and
  `func (k Kind) Broadcastable() bool` / `Valid() bool`.
- Building a `types.Hash` from `e.Reference` (`copy(ref[:], e.Reference)`) appears in `frameEvent` and `receive`. Add `refOf(e)`.

### 4.4 Stream session lifecycle (`stream.go`)
`Exchange` (`:309`) and `dialSession` (`:349`) both: build `peerSession{send, done, cancel}`, `installSession`,
spawn a `readStream` goroutine with identical error logging, then `removeSession`.
**Do:** `p.runSession(ctx, cancel, peer, send, recv) (*peerSession, bool)`. Each caller keeps only its own
handshake direction.

### 4.5 Config / generate
- `LoadConfig` (`config.go:61-75`) and `Generate` (`generate.go:432-446`) declare the **same anonymous 13-field
  struct** for `network.json`. Define `type NetworkFile struct` once. `LoadConfig` then unmarshals into it and
  copies it with one assignment of the embedded groups from 3.5.
- Default values live in three places that disagree:
  - `MinBlockTXs`: `Defaults()` = 3, `Generate` = 3, `runtime.New` = **1**.
  - `BlockMaxWait` / `BaseTimeout` = 2000 in both `Defaults()` and `Generate`.
  - `(4 << 20) - (16 << 10)` appears 3× (`config.go:148, 166`, `generate.go:446`).

  Add `const MaxBlockBytes`, `DefaultMinBlockTXs`, … in one file, and have `Generate` write `Defaults()` output instead of literals.
- Mempool-limit validation is repeated in `runtime.New:91`, `Config.Validate:166` and `Generate:414`. Use
  `MempoolLimits.Validate()`.
- The `n = 3f+1` check is repeated in `consensus.New`, `Config.Validate`, `Generate`, and `run.py load_configs`. Add one Go
  helper, `types.CheckValidatorCount(n, f)`. (Python must keep its own copy; give it a pointer comment.)

### 4.6 Logging policy (`logWriter.record`)
The filter at `run.go:48-71` combines three rules (an "important" allow-list, a reject-reason allow-list, and a
rate-limited set), each with string matching. Make the policy a table:
```go
var logPolicy = map[string]struct{ always, rateLimited, keyByReason bool }{...}
```
`p.Record` vs `p.log.record` (`run.go:297` bypasses metrics counting) is probably an accident. Settle on one entry point.
Also give `process` an `event(kind, peer, …)` helper like 3.3: `p.Record(network.Event{Node: p.cfg.NodeID, …})` is written out about 15×.

### 4.7 Admin HTTP
- Hand-written Prometheus output (`admin.go:376-384`, 9 nearly identical `Fprintf` calls). Use a
  `gauge(w, name, help, value)` helper.
- `lab_status`/`lab_state` and `/status`/`/state` build similar views. Have both REST and RPC call
  `p.statusView()`/`p.makeStateView()`.
- `submit.go` mixes server-side ingress with two client libraries (`JSONRPCClient`, `TransactionClient`).
  Move the clients to `realnet/client.go` (or `src/client`) so the server file holds only ingress.
- `JSONRPCClient` reuses `rpcError` from `admin.go`. Keep the shared RPC types in `rpc_types.go`.

---

## 5. Python test tooling (`tests/`)

### 5.1 Scenario boilerplate (10 scripts × the same ~6 lines)
Every scenario except T8 repeats:
```python
with Cluster(NAME, NETWORK, NODES) as net:
    for nonce in range(1, COUNT + 1):
        net.submit(nonce, key="spam", value=f"value-{nonce}", via=9)
    net.wait_finalized(COUNT, ...)
    net.check_agreement(COUNT)
```
**Do:** add `Cluster.spam(count, via=9)`, and a `scenario(name, network, nodes, count, checks, wait_nodes=None)`
driver in `harness.py` that runs `spam → wait → agreement → checks(net)` and calls `finish`. Each scenario file
then contains only its fault config and its specific checks.
Also deduplicate:
- `NETWORK = {"BaseTimeoutMS": 1000}` appears in 9 files. Make it the harness default.
- The T3a and T3b bodies are identical apart from the behaviour name. Use one shared check function.
- The T6a and T6b bodies are identical apart from the behaviour name. Use a `check_timeout_recovered(net)` helper.
- The `consensus_vote_signed` counting in T2 should become a harness query: `net.votes_signed_per_round(node)`.

### 5.2 Harness vs launcher duplication
- `Cluster._create` shells out to `python tests/run.py --init` (`harness.py:56`) when it could call
  `launcher.prepare_cluster(...)` directly.
- `Cluster.start/stop` re-implement process launch/termination already in `run.py` (`run_cluster`,
  `stop_processes`). Expose `launcher.start_nodes(binary, configs) -> procs` and reuse `stop_processes`.
- Admin URL resolution is inconsistent. `submit` rewrites `0.0.0.0` → `127.0.0.1` (`harness.py:121`), but `_get`
  uses the raw `AdminAddress` (`:140`), and `prepare_monitoring.admin_address` has a third variant (it prefers the runtime
  `admin` file). Use one `launcher.admin_url(path, config, output)` everywhere.
- `run.py` builds the binary path in two places (`build_node` and `main:248`). Add `binary_path()`.

### 5.3 `run_tests.py`
The `go test -json` streaming block (`run_tests.py:313-352`) and the scenario loop are two separate concerns in `main()`.
Split them into `run_go_tests(...)`, `run_vet(...)` and `run_scenarios(...)`, with `summary.json` written once at the end (today it is written in 3 places).

---

## 6. Naming / small hygiene

- `runtime/runtime.go:1` says `// Package node`, but the package is `runtime`. The name also shadows Go's `runtime`,
  which forces the `runtime "…/src/runtime"` aliases. Consider renaming it to `node` (path `src/node`).
- `crypt` alias for `blockchain-lab01/src/crypto` is used everywhere because of the stdlib clash. Either keep it
  (consistently) or rename the package `lcrypto`/`sig`.
- `network` package only holds wire types and errors; it does no networking. Rename it to `wire`, or merge it into `types`.
- `realnet.Config` and `runtime.Config` have one-line field lists padded with about 120 spaces of alignment, the
  result of 9 ints declared on one line. Splitting them per group (3.5) fixes this.
- `.gitignore` says "Generated local clusters contain private keys", yet `config/*/key-*.hex` are committed.
  This is fine for test keys, but make the comment and README say so explicitly.

---

## 7. Issues found while reading (not pure refactors: decide separately)

| # | Where | Observation |
|---|---|---|
| 1 | `tests/scenarios/t8_same_seed.py` | Spec §8/T8 requires **byte-identical logs** plus the same state hash. The script compares only the tx set and the root (its docstring admits this). That is a grading risk. Byte-identical logs would need logical timestamps (`Seq`, not wall clock) and a deterministic log subset. |
| 2 | `runtime.New:106` vs `realnet.Defaults:141` | `MinBlockTXs` defaults to 1 in one and 3 in the other. Direct runtime users and real nodes batch differently. |
| 3 | `realnet/run.go:181` `seen/accepted/requested` | These maps are never pruned, so they grow without bound on long runs. |
| 4 | `runtime.go:282-288` `ads` | Decision adverts are kept forever and re-announced every 4th retry, so retry cost grows linearly with chain height. Keep only the last *k* decisions, or serve old ones on request only. |
| 5 | `run.go:297` | `acceptFrame` logs via `p.log.record`, which bypasses `eventCounts`. `reject_transport` never reaches `/metrics`. |
| 6 | Engine double validation (2.4) | Performance only: each proposal's proof signatures are verified twice per node. |

---

## Suggested order and size

| Step | Phase | Risk | Est. diff |
|---|---|---|---|
| 1 | 0: vectors + baseline | none | +150 |
| 2 | 1.1–1.3, 1.5: clone / chain-ID / key helpers | low | −80 net |
| 3 | 1.4: shared encoder (vector-guarded) | medium (bytes) | −60 net |
| 4 | 2.1–2.3, 2.5: engine dedupe + file split | medium | −100 net |
| 5 | 2.4: proposal/decision validation reuse | medium | −30 net |
| 6 | 3.1: adversary extraction + `Behavior` type | medium | ±0, much cleaner |
| 7 | 3.2–3.4: runtime split, `wireHeader` methods | low | −60 net |
| 8 | 3.5 + 4.5: grouped configs, `NetworkFile`, constants | medium (JSON compat) | −80 net |
| 9 | 4.1–4.4, 4.6–4.7: realnet file split + helpers | low | −120 net |
| 10 | 5: Python harness + scenarios | low | −150 net |
| 11 | 6 + chosen items from §7 | varies | — |

Each step is one commit. After each one, run `gofmt -l src`, `go vet ./...`, `go test ./...`, and the T1–T8 suite
(or at least T1 + T7 for quick feedback).
