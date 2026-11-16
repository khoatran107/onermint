package realnet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

type runtimeSnapshot struct {
	transactions []types.Transaction
	root         types.Hash
	status       cons.Status
	accountNonce uint64
	err          error
}

type snapshotQuery struct {
	sender *types.PublicKey
	reply  chan runtimeSnapshot
}

type pendingTransaction struct {
	TransactionID string
	Transaction   types.Transaction
}

func (p *process) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", p.handleJSONRPC)
	mux.HandleFunc("/metrics", p.handleMetrics)
	mux.HandleFunc("/mempool", p.handleMempool)
	mux.HandleFunc("/ledger", p.handleLedger)
	mux.HandleFunc("/status", p.handleStatus)
	return mux
}

func (p *process) snapshot(ctx context.Context, sender *types.PublicKey) (runtimeSnapshot, error) {
	reply := make(chan runtimeSnapshot, 1)
	select {
	case p.in <- received{query: &snapshotQuery{sender: sender, reply: reply}}:
	case <-ctx.Done():
		return runtimeSnapshot{}, ctx.Err()
	case <-p.ctx.Done():
		return runtimeSnapshot{}, fmt.Errorf("node stopping")
	}
	select {
	case snapshot := <-reply:
		return snapshot, snapshot.err
	case <-ctx.Done():
		return runtimeSnapshot{}, ctx.Err()
	case <-p.ctx.Done():
		return runtimeSnapshot{}, fmt.Errorf("node stopping")
	}
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "GET required", http.StatusMethodNotAllowed)
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAdminError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, struct{ Error string }{err.Error()})
}

func (p *process) handleMempool(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snapshot, err := p.snapshot(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err)
		return
	}
	entries := make([]pendingTransaction, 0, len(snapshot.transactions))
	for _, tx := range snapshot.transactions {
		id, err := crypt.TransactionID(tx)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err)
			return
		}
		entries = append(entries, pendingTransaction{TransactionID: hex.EncodeToString(id[:]), Transaction: tx})
	}
	writeJSON(w, http.StatusOK, struct {
		NodeID       int
		Count        int
		Transactions []pendingTransaction
	}{p.cfg.NodeID, len(entries), entries})
}

func (p *process) handleLedger(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	from, limit, err := ledgerRange(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err)
		return
	}
	all, err := p.store.ReadAll()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err)
		return
	}
	blocks := selectLedgerRange(all, from, limit)
	writeJSON(w, http.StatusOK, struct {
		NodeID int
		Height uint64
		From   uint64
		Count  int
		Blocks []cons.Finalized
	}{p.cfg.NodeID, p.store.Height(), from, len(blocks), blocks})
}

func (p *process) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snapshot, err := p.snapshot(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		NodeID      int
		ChainID     string
		Height      uint64
		MempoolSize int
		StateRoot   string
		Consensus   cons.Status
	}{p.cfg.NodeID, p.cfg.ChainID, p.store.Height(), len(snapshot.transactions), hex.EncodeToString(snapshot.root[:]), snapshot.status})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcLockStatus struct {
	Round     uint64 `json:"round"`
	BlockHash string `json:"blockHash"`
}

type rpcNodeStatus struct {
	NodeID              int            `json:"nodeId"`
	ChainID             string         `json:"chainId"`
	FinalizedHeight     uint64         `json:"finalizedHeight"`
	ConsensusHeight     uint64         `json:"consensusHeight"`
	Round               uint64         `json:"round"`
	Step                string         `json:"step"`
	MempoolTransactions int            `json:"mempoolTransactions"`
	Lock                *rpcLockStatus `json:"lock"`
}

func allowRPC(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
}

func (p *process) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	allowRPC(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		writeRPC(w, nil, nil, &rpcError{-32600, "POST required"})
		return
	}
	reader := io.LimitReader(r.Body, int64(p.cfg.MaxTxBytes)+16*1024)
	var request rpcRequest
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.JSONRPC != "2.0" || request.Method == "" || len(request.ID) == 0 {
		writeRPC(w, request.ID, nil, &rpcError{-32600, "invalid JSON-RPC 2.0 request"})
		return
	}
	result, rpcErr := p.callRPC(r.Context(), request.Method, request.Params)
	writeRPC(w, request.ID, result, rpcErr)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

func (p *process) callRPC(ctx context.Context, method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "lab_chainId":
		return p.cfg.ChainID, nil
	case "lab_status":
		snapshot, err := p.snapshot(ctx, nil)
		if err != nil {
			return nil, &rpcError{-32000, err.Error()}
		}
		status := rpcNodeStatus{NodeID: p.cfg.NodeID, ChainID: p.cfg.ChainID, FinalizedHeight: p.store.Height(), ConsensusHeight: snapshot.status.Height, Round: snapshot.status.Round, Step: consensusStepName(snapshot.status.Step), MempoolTransactions: len(snapshot.transactions)}
		if snapshot.status.Locked {
			status.Lock = &rpcLockStatus{Round: snapshot.status.LockedRound, BlockHash: "0x" + hex.EncodeToString(snapshot.status.LockedHash[:])}
		}
		return status, nil
	case "lab_getAccount":
		var params struct {
			Sender string `json:"sender"`
		}
		if err := decodeRPCParams(raw, &params); err != nil {
			return nil, &rpcError{-32602, err.Error()}
		}
		bytes, err := hex.DecodeString(params.Sender)
		if err != nil || len(bytes) != 32 || params.Sender != strings.ToLower(params.Sender) {
			return nil, &rpcError{-32602, "sender must be 32-byte lowercase hex"}
		}
		var sender types.PublicKey
		copy(sender[:], bytes)
		snapshot, err := p.snapshot(ctx, &sender)
		if err != nil {
			return nil, &rpcError{-32000, err.Error()}
		}
		next := snapshot.accountNonce + 1
		pending := make(map[uint64]bool)
		for _, tx := range snapshot.transactions {
			if tx.Sender == sender {
				pending[tx.Nonce] = true
			}
		}
		for pending[next] {
			next++
		}
		return map[string]any{"committedNonce": snapshot.accountNonce, "nextNonce": next}, nil
	case "lab_sendRawTransaction":
		var params struct {
			Transaction types.Transaction `json:"transaction"`
		}
		if err := decodeRPCParams(raw, &params); err != nil {
			return nil, &rpcError{-32602, err.Error()}
		}
		id, err := p.submitTransaction(ctx, params.Transaction)
		if err != nil {
			return nil, &rpcError{-32010, err.Error()}
		}
		return "0x" + hex.EncodeToString(id[:]), nil
	default:
		return nil, &rpcError{-32601, "method not found"}
	}
}

func consensusStepName(step cons.Step) string {
	switch step {
	case cons.Propose:
		return "PROPOSE"
	case cons.Prevote:
		return "PREVOTE"
	case cons.Precommit:
		return "PRECOMMIT"
	default:
		return "UNKNOWN"
	}
}

func decodeRPCParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("params object required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func (p *process) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snapshot, err := p.snapshot(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	p.metricsMu.RLock()
	counts := make(map[string]uint64, len(p.eventCounts))
	for event, count := range p.eventCounts {
		counts[event] = count
	}
	p.metricsMu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	label := fmt.Sprintf("node=\"%d\",chain=\"%s\"", p.cfg.NodeID, strings.ReplaceAll(p.cfg.ChainID, "\"", "\\\""))
	fmt.Fprintf(w, "# HELP lab01_ledger_height Last finalized block height.\n# TYPE lab01_ledger_height gauge\nlab01_ledger_height{%s} %d\n", label, p.store.Height())
	fmt.Fprintf(w, "# HELP lab01_consensus_height Block height currently being processed.\n# TYPE lab01_consensus_height gauge\nlab01_consensus_height{%s} %d\n", label, snapshot.status.Height)
	fmt.Fprintf(w, "# HELP lab01_mempool_transactions Transactions waiting in RAM.\n# TYPE lab01_mempool_transactions gauge\nlab01_mempool_transactions{%s} %d\n", label, len(snapshot.transactions))
	fmt.Fprintf(w, "# HELP lab01_outbound_queue Messages waiting for transmission.\n# TYPE lab01_outbound_queue gauge\nlab01_outbound_queue{%s} %d\n", label, p.queued.Load())
	fmt.Fprintf(w, "# HELP lab01_config_min_block_transactions Configured transaction threshold.\n# TYPE lab01_config_min_block_transactions gauge\nlab01_config_min_block_transactions{%s} %d\n", label, p.cfg.MinBlockTXs)
	fmt.Fprintf(w, "# HELP lab01_config_block_max_wait_milliseconds Configured maximum batch wait.\n# TYPE lab01_config_block_max_wait_milliseconds gauge\nlab01_config_block_max_wait_milliseconds{%s} %d\n", label, p.cfg.BlockMaxWaitMS)
	fmt.Fprintf(w, "# HELP lab01_consensus_round Current consensus round.\n# TYPE lab01_consensus_round gauge\nlab01_consensus_round{%s} %d\n", label, snapshot.status.Round)
	fmt.Fprintf(w, "# HELP lab01_consensus_step Current step: propose=1, prevote=2, precommit=3.\n# TYPE lab01_consensus_step gauge\nlab01_consensus_step{%s} %d\n", label, snapshot.status.Step)
	fmt.Fprintf(w, "# HELP lab01_consensus_locked Whether this validator is locked.\n# TYPE lab01_consensus_locked gauge\nlab01_consensus_locked{%s} %d\n", label, boolMetric(snapshot.status.Locked))
	fmt.Fprintln(w, "# HELP lab01_events_total Node events observed since process start.\n# TYPE lab01_events_total counter")
	for event, count := range counts {
		fmt.Fprintf(w, "lab01_events_total{%s,event=\"%s\"} %d\n", label, strings.ReplaceAll(event, "\"", "\\\""), count)
	}
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ledgerRange(r *http.Request) (uint64, int, error) {
	from := uint64(1)
	limit := 0
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		from, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || from == 0 {
			return 0, 0, fmt.Errorf("from must be a positive block height")
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit64, parseErr := strconv.ParseUint(raw, 10, 31)
		if parseErr != nil || limit64 == 0 {
			return 0, 0, fmt.Errorf("limit must be positive")
		}
		limit = int(limit64)
	}
	return from, limit, nil
}

func selectLedgerRange(all []cons.Finalized, from uint64, limit int) []cons.Finalized {
	start := len(all)
	for i := range all {
		if all[i].Block.Header.Height >= from {
			start = i
			break
		}
	}
	end := len(all)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	out := make([]cons.Finalized, end-start)
	copy(out, all[start:end])
	return out
}
