# AGENTS.md

Guidance for AI coding agents working in this repository. Human-facing docs are in
`README.md` (Vietnamese); the assignment spec is `Lab01.pdf` (gitignored, may be absent).

## What this is

A course lab (Blockchain 2026, Lab 01): a minimal permissioned Layer-1 blockchain with BFT
finality. `n = 3f + 1` validators (default `n = 10`, `f = 3`, quorum `2f + 1 = 7`) run as
separate Go processes on localhost, and talk over GossipSub (headers, votes, transactions)
and gRPC bidirectional streams (block bodies). Consensus is a Tendermint-style
**Propose → PREVOTE → PRECOMMIT** round protocol with locking. State is an owned
key/value map with per-sender nonces, committed by a Merkle root. Finalized blocks are
stored in each node's LevelDB.

## Commands

```sh
gofmt -l src                      # must print nothing
go vet ./...
go test ./... -count=1            # ~5 s; add -race before finishing a change
go test ./src/consensus -run TestName -count=1

python3 tests/test_vectors.py     # Python client vs Go golden bytes (needs `cryptography`)
python3 tests/scenarios/t1_normal.py   # one end-to-end scenario: starts 10 real processes
python3 tests/run_tests.py --race --output local-runs/<fresh-dir>   # everything; ~10 min
```

- Go version comes from `go.mod`; set `GOTOOLCHAIN=auto` if the local Go is older.
- Scenarios bind fixed ports (gRPC 29000+, GossipSub 30000+, HTTP 31000+). **Never run two
  scenarios or suites at the same time**, and stop any manually started cluster first.
- `run_tests.py --output` requires a directory that does not exist yet.
- `local-runs/` holds the built binary, ledgers and logs. It is gitignored and safe to delete.

## Layout

| Path | Responsibility |
|---|---|
| `src/types` | Wire types (`Transaction`, `Block`, `Vote`), `Clone` helpers, `CheckValidatorCount` |
| `src/crypto` | Ed25519 sign/verify, SHA-256, **canonical encoding** (`Encoder`, domain constants) |
| `src/state` | Execution (ownership + nonce rules), Merkle state root |
| `src/block` | `Build`, `ValidateHeader`, `ValidateBody`, `Validate` |
| `src/mempool` | Bounded RAM pool (`Limits`), deterministic selection |
| `src/ledger` | LevelDB store of finalized blocks |
| `src/consensus` | Single-threaded engine: `engine.go` (steps, timeouts, votes), `proposal.go`, `decision.go` (finalize/restore/catch-up), `votes.go` (tallies, certificate checks) |
| `src/runtime` | IO-free node logic driving the engine through a `Port`: `runtime.go` (process loop, message handlers), `batching.go` (mempool admission, block wait), `wire.go` (`WireHeader`), `adversary.go` (**all** Byzantine behaviours) |
| `src/realnet` | Real process: `run.go` (lifecycle, owner loop), `inbound.go`/`outbound.go`, `scheduler.go`, `eventlog.go`, `gossip.go`, `stream.go`, `faults.go` (network fault injection), `config.go`/`generate.go`, `admin.go`/`rpc.go` (HTTP + JSON-RPC), `submit.go`/`client.go` |
| `src/network` | Transport message kinds, `Event` log record, `Rejection` |
| `src/proto` | gRPC envelope. `*.pb.go` are generated: edit `consensus.proto` and regenerate with `protoc`, never edit the generated files by hand |
| `src/cmd/node` | Binary entry point (`-config`, `-init-dir`, `-inspect`) |
| `tests/run.py` | Cluster launcher and shared helpers (`load_configs`, `start_nodes`, `admin_url`, …) |
| `tests/scenarios/` | T1–T8 end-to-end scenarios on top of `harness.py` |
| `tests/client.py` | Python Ed25519 + JSON-RPC client used by the harness |
| `config/<scenario>/` | Committed scenario configs **and test-only keys** |
| `client/` | Static browser client (Vercel). Out of scope unless asked |
| `logs/monitoring/` | Grafana/Prometheus/Loki stack |

## Invariants: do not break these

1. **Canonical bytes.** Every signature and hash is computed over the bytes produced by
   `src/crypto/encoding.go` (plus `CanonicalProposalBytes`, `TransportBytes` and
   `State.Root`). `src/crypto/testdata/vectors.json` pins them. If `TestGoldenVectors`
   fails, the change is wrong. Do **not** run `-update` unless the user explicitly asks
   for a format change. A format change invalidates every committed config and key, and
   breaks `tests/client.py` and `client/app.js`.
2. **Domain separation.** Every signed payload starts with `"<DOMAIN>:<chainID>"`, using the
   `crypt.Domain*` constants. A new signed message type needs a new domain constant.
3. **Determinism.** Never let map iteration order decide an outcome, a proof order or an
   output. Iterate the validator list or sort, as `rounds()`, `roundChoices()`,
   `certificate()` and `mempool.sorted()` do. The spec requires reproducible runs.
4. **Engine safety rules** (spec §6.1). At most one PREVOTE and one PRECOMMIT per
   (height, round) for this node (`emit` + `sent`). Lock on a PREVOTE quorum (`observeQuorum`).
   Prevote NIL when locked on another block. Count at most one vote per validator per
   (round, phase). Detect equivocation and never double-count it. Finalize only with a
   PRECOMMIT quorum **and** a known, executed block.
5. **The engine is single-threaded and IO-free.** It returns work through `Drain()`
   (votes, proposals, timers, events, decisions). Do not add goroutines, clocks or sockets
   to `src/consensus` or `src/runtime`. All realnet state is owned by the loop in `run.go`;
   other goroutines reach it only through `ownerCall`.
6. **Header before body.** A body is accepted only after the receiver accepted the matching
   header (`seen`/`accepted`/`requested` in realnet, `remote` in runtime).
7. **Copy, don't share.** Values crossing a package boundary are cloned
   (`Transaction.Clone`, `Block.Clone`, `cloneProposal`, `cloneFinalized`).
8. **Observable names are an API.** Event names (`consensus_vote_signed`, `fault_drop`,
   `behavior_message`, `state_commit`, …), reject reason strings and the JSON shapes of
   `/status`, `/ledger` and JSON-RPC are read by the scenario scripts and the Grafana
   dashboard (`logs/monitoring/grafana/dashboards/lab01.json`). Rename them only together
   with every reader.
9. **Behaviour names and exit code 86** (`runtime.CrashExitCode`) appear in committed
   `config/*/node-*.json` and in `tests/run.py`.
10. **At most `f` non-honest validators** per configuration (`tests/run.py` enforces this).

## Conventions

- `gofmt` is required; `run_tests.py` fails on unformatted code.
- Errors: peer or client input that is invalid → `network.Reject(err)` (logged, node keeps
  running). Anything else returned from the owner loop is fatal and stops the node.
  `consensus.ErrPersistence` is always fatal.
- Log through the helpers: `n.record` / `n.recordPeer` in runtime, `p.event` / `p.Record` in
  realnet. Do not write `network.Event{Node: ..., Peer: -1, ...}` by hand.
- Byzantine behaviour lives **only** in `src/runtime/adversary.go`, behind hooks that are
  no-ops for `honest`. The honest code must never test a behaviour name.
- Config groups (`mempool.Limits`, `runtime.BlockLimits`) are embedded in `realnet.Config`
  so the JSON stays flat. Keep new fields JSON-compatible with existing configs.
  Defaults are named constants in `src/realnet/config.go`.
- Comments explain *why* (protocol rule, attack prevented), not *what*.
- Python: standard library plus `cryptography`; Python 3.9+.
- User-facing docs (`README.md`) are written in Vietnamese; code comments are in English.

## Adding a scenario or a Byzantine behaviour

Follow README → "Thêm kịch bản với behaviour mới". In short:

1. Add a `Behavior` constant in `src/runtime/adversary.go`, add it to `Valid()`, and
   implement it in an existing hook (`proposerTurn`, `tamperProposal`, `tamperVote`,
   `afterVote`, `afterAdmit`, `afterFinalize`, `duplicateGossip`). Add a new hook only if
   none fits, and call it from exactly one place in `runtime.go`.
2. A tampered message must stay **authentic** (re-sign it) unless the test is about bad
   signatures. Otherwise peers reject it for the wrong reason.
3. Record a `behavior_message` event so the scenario can prove the fault really happened.
4. Write `tests/scenarios/tN_<name>.py` with `scenario(...)` and a `checks(net)` function.
   `run_tests.py` picks up `t*.py` automatically.
5. Gotcha: `scenario(network=..., nodes=...)` applies **only when `config/<name>/` does not
   exist yet**. After the first run, the JSON files in that folder are the source of truth.
   Delete the folder to regenerate it (this creates new keys), then commit it.
6. When matching reject reasons, use specific substrings: `"nonce"` alone also matches the
   mempool's `replayed transaction nonce …` messages.

## Before you finish a change

1. `gofmt -l src` prints nothing, `go vet ./...` is clean, and `go test ./... -race -count=1` passes.
2. Touched encoding, the engine, runtime or realnet → run the scenarios they affect, or the
   full `run_tests.py`. Report the actual PASS/FAIL lines.
3. Touched a JSON shape, event name or behaviour → update the readers (scenarios,
   dashboard, README).
4. Do not commit `local-runs/`, `tests/wallets/` or new private keys outside `config/`.

## Known gaps (not bugs to fix silently; ask first)

- T8 compares finalized transactions and state root, not byte-identical logs as the spec asks.
- `MinBlockTXs` defaults to 1 in `runtime.New` but 3 in realnet configs.
- `seen`/`accepted`/`requested` maps in realnet are never pruned.
- Old decision headers are re-announced forever, so retry cost grows with height.
- Voting and lock state of the interrupted height is not persisted across a restart (see README, Full sync).

## Submission

The graded zip must contain only `src/ tests/ logs/ config/ README.md REPORT.pdf`. A wrong
layout scores zero. Files such as `AGENTS.md` and `REFACTOR_PLAN.md` must be left out of the zip.
