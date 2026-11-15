package realnet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	crypt "blockchain-lab01/src/crypto"
	pb "blockchain-lab01/src/proto"
	"blockchain-lab01/src/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func (p *process) Submit(ctx context.Context, request *pb.SubmitRequest) (*pb.SubmitReply, error) {
	if len(request.Transaction) > p.cfg.MaxTxBytes {
		return nil, status.Error(codes.ResourceExhausted, "transaction too large")
	}
	var tx types.Transaction
	if err := json.Unmarshal(request.Transaction, &tx); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id, err := p.submitTransaction(ctx, tx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &pb.SubmitReply{TransactionId: id[:]}, nil
}

// JSONRPCClient is the public client interface used by the browser and load
// generator. Validator-to-validator traffic does not use this client.
type JSONRPCClient struct {
	url    string
	client *http.Client
	nextID uint64
}

type JSONRPCError struct {
	Code    int
	Message string
}

func (e *JSONRPCError) Error() string { return fmt.Sprintf("JSON-RPC %d: %s", e.Code, e.Message) }

func NewJSONRPCClient(baseURL string, timeout time.Duration) (*JSONRPCClient, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("positive JSON-RPC timeout required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return &JSONRPCClient{url: baseURL + "/rpc", client: &http.Client{Timeout: timeout}}, nil
}

func (c *JSONRPCClient) Submit(ctx context.Context, tx types.Transaction) (types.Hash, error) {
	c.nextID++
	requestBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": "lab_sendRawTransaction",
		"params": map[string]any{"transaction": tx},
	})
	if err != nil {
		return types.Hash{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(requestBody))
	if err != nil {
		return types.Hash{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return types.Hash{}, err
	}
	defer response.Body.Close()
	var body struct {
		Result string    `json:"result"`
		Error  *rpcError `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return types.Hash{}, err
	}
	if body.Error != nil {
		return types.Hash{}, &JSONRPCError{Code: body.Error.Code, Message: body.Error.Message}
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(body.Result, "0x"))
	if err != nil || len(raw) != 32 {
		return types.Hash{}, fmt.Errorf("invalid JSON-RPC transaction receipt")
	}
	return types.Hash(raw), nil
}

// submitTransaction is the one ingress path shared by gRPC and JSON-RPC.
// Successful return means admission to this node's RAM mempool, not finality.
func (p *process) submitTransaction(ctx context.Context, tx types.Transaction) (types.Hash, error) {
	var zero types.Hash
	r := received{tx: &tx, done: make(chan error, 1)}
	select {
	case p.in <- r:
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.ctx.Done():
		return zero, fmt.Errorf("node stopping")
	}
	select {
	case err := <-r.done:
		if err != nil {
			return zero, err
		}
		id, err := crypt.TransactionID(tx)
		if err != nil {
			return zero, err
		}
		return id, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-p.ctx.Done():
		return zero, fmt.Errorf("node stopping")
	}
}

// SubmitTransaction is client ingress over a real local gRPC connection.
// Successful admission does not mean the transaction has finalized.
func SubmitTransaction(ctx context.Context, address string, tx types.Transaction) (types.Hash, error) {
	client, err := NewTransactionClient(address)
	if err != nil {
		return types.Hash{}, err
	}
	defer client.Close()
	return client.Submit(ctx, tx)
}

// TransactionClient reuses one gRPC connection for clients that submit many
// transactions. It is client ingress only; validators still propagate accepted
// transactions to peers through GossipSub.
type TransactionClient struct {
	conn   *grpc.ClientConn
	client pb.TransportClient
}

func NewTransactionClient(address string) (*TransactionClient, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &TransactionClient{conn: conn, client: pb.NewTransportClient(conn)}, nil
}

func (c *TransactionClient) Close() error { return c.conn.Close() }

func (c *TransactionClient) Submit(ctx context.Context, tx types.Transaction) (types.Hash, error) {
	var zero types.Hash
	b, err := json.Marshal(tx)
	if err != nil {
		return zero, err
	}
	reply, err := c.client.Submit(ctx, &pb.SubmitRequest{Transaction: b})
	if err != nil {
		return zero, err
	}
	if len(reply.TransactionId) != 32 {
		return zero, fmt.Errorf("invalid transaction receipt")
	}
	return types.Hash(reply.TransactionId), nil
}

func SubmitValue(ctx context.Context, c Config, record, value string, nonce uint64, via int) (types.Hash, error) {
	_, key, err := c.keys()
	if err != nil {
		return types.Hash{}, err
	}
	pub := types.PublicKey(key[32:])
	tx, err := crypt.SignTransaction(types.Transaction{ChainID: c.ChainID, Sender: pub, TargetKey: hex.EncodeToString(pub[:]) + "/" + record, Value: []byte(value), Nonce: nonce}, key)
	if err != nil {
		return types.Hash{}, err
	}
	if via < 0 {
		via = c.NodeID
	}
	if via >= len(c.Peers) {
		return types.Hash{}, fmt.Errorf("unknown ingress node")
	}
	return SubmitTransaction(ctx, c.Peers[via].Address, tx)
}
