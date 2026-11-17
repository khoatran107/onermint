package realnet

import (
	"context"
	"encoding/json"

	crypt "blockchain-lab01/src/crypto"
	pb "blockchain-lab01/src/proto"
	"blockchain-lab01/src/types"
	"google.golang.org/grpc/codes"
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

// submitTransaction is the one ingress path shared by gRPC and JSON-RPC.
// Successful return means admission to this node's RAM mempool, not finality.
func (p *process) submitTransaction(ctx context.Context, tx types.Transaction) (types.Hash, error) {
	r := received{tx: &tx, done: make(chan error, 1)}
	result, err := ownerCall(p, ctx, r, r.done, errNodeStopping)
	if err == nil {
		err = result
	}
	if err != nil {
		return types.Hash{}, err
	}
	return crypt.TransactionID(tx)
}
