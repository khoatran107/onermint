package realnet

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

type runtimeSnapshot struct {
	transactions []types.Transaction
	state        state.Snapshot
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
	mux.HandleFunc("/state", p.handleState)
	return mux
}

type stateRecordView struct {
	Key         string `json:"key"`
	ValueBase64 string `json:"valueBase64"`
}

type stateAccountView struct {
	Sender         string `json:"sender"`
	CommittedNonce uint64 `json:"committedNonce"`
}

type stateView struct {
	NodeID    int                `json:"nodeId"`
	ChainID   string             `json:"chainId"`
	Height    uint64             `json:"height"`
	StateRoot string             `json:"stateRoot"`
	Records   []stateRecordView  `json:"records"`
	Accounts  []stateAccountView `json:"accounts"`
}

func (p *process) makeStateView(snapshot runtimeSnapshot) stateView {
	view := stateView{NodeID: p.cfg.NodeID, ChainID: p.cfg.ChainID, Height: p.store.Height(), StateRoot: hex.EncodeToString(snapshot.root[:]), Records: make([]stateRecordView, 0, len(snapshot.state.Records)), Accounts: make([]stateAccountView, 0, len(snapshot.state.Accounts))}
	for _, record := range snapshot.state.Records {
		view.Records = append(view.Records, stateRecordView{Key: record.Key, ValueBase64: base64.StdEncoding.EncodeToString(record.Value)})
	}
	for _, account := range snapshot.state.Accounts {
		view.Accounts = append(view.Accounts, stateAccountView{Sender: hex.EncodeToString(account.Sender[:]), CommittedNonce: account.Nonce})
	}
	return view
}

func (p *process) handleState(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	snapshot, err := p.snapshot(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, p.makeStateView(snapshot))
}

func (p *process) snapshot(ctx context.Context, sender *types.PublicKey) (runtimeSnapshot, error) {
	reply := make(chan runtimeSnapshot, 1)
	snapshot, err := ownerCall(p, ctx, received{query: &snapshotQuery{sender: sender, reply: reply}}, reply, errNodeStopping)
	if err != nil {
		return runtimeSnapshot{}, err
	}
	return snapshot, snapshot.err
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
	gauge := func(name, help string, value any) {
		fmt.Fprintf(w, "# HELP lab01_%s %s\n# TYPE lab01_%s gauge\nlab01_%s{%s} %d\n", name, help, name, name, label, value)
	}
	gauge("ledger_height", "Last finalized block height.", p.store.Height())
	gauge("consensus_height", "Block height currently being processed.", snapshot.status.Height)
	gauge("mempool_transactions", "Transactions waiting in RAM.", len(snapshot.transactions))
	gauge("outbound_queue", "Messages waiting for transmission.", p.queued.Load())
	gauge("config_min_block_transactions", "Configured transaction threshold.", p.cfg.MinBlockTXs)
	gauge("config_block_max_wait_milliseconds", "Configured maximum batch wait.", p.cfg.BlockMaxWaitMS)
	gauge("consensus_round", "Current consensus round.", snapshot.status.Round)
	gauge("consensus_step", "Current step: propose=1, prevote=2, precommit=3.", snapshot.status.Step)
	gauge("consensus_locked", "Whether this validator is locked.", boolMetric(snapshot.status.Locked))
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
