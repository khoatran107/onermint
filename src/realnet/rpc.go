package realnet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"blockchain-lab01/src/types"
)

// JSON-RPC 2.0 endpoint (POST /rpc) used by the browser and test clients.

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
		status := rpcNodeStatus{NodeID: p.cfg.NodeID, ChainID: p.cfg.ChainID, FinalizedHeight: p.store.Height(), ConsensusHeight: snapshot.status.Height, Round: snapshot.status.Round, Step: snapshot.status.Step.String(), MempoolTransactions: len(snapshot.transactions)}
		if snapshot.status.Locked {
			status.Lock = &rpcLockStatus{Round: snapshot.status.LockedRound, BlockHash: "0x" + hex.EncodeToString(snapshot.status.LockedHash[:])}
		}
		return status, nil
	case "lab_state":
		snapshot, err := p.snapshot(ctx, nil)
		if err != nil {
			return nil, &rpcError{-32000, err.Error()}
		}
		return p.makeStateView(snapshot), nil
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
