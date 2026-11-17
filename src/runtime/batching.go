package runtime

import (
	"encoding/json"
	"fmt"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/types"
)

// Submit is client ingress: admit locally, remember the transaction for
// periodic re-gossip while it is pending, and gossip it once now.
func (n *Runtime) Submit(tx types.Transaction) error {
	if err := n.admit(tx); err != nil {
		return network.Reject(err)
	}
	id, err := crypt.TransactionID(tx)
	if err != nil {
		return err
	}
	n.localTX[id] = tx.Clone()
	if err := n.gossipTX(tx); err != nil {
		return err
	}
	if err := n.duplicateGossip(tx, 1); err != nil {
		return err
	}
	return n.process()
}

func (n *Runtime) gossipTX(tx types.Transaction) error {
	b, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return n.broadcast(network.Message{Kind: network.Transaction, Data: b})
}

// admit adds tx to the mempool with a TTL eviction timer.
func (n *Runtime) admit(tx types.Transaction) error {
	id, err := n.pool.Admit(n.chain, n.engine.State(), tx)
	if err != nil {
		n.record(network.Event{Event: "transaction_reject", Reason: err.Error()})
		return err
	}
	n.afterAdmit(tx)
	if err := n.port.Schedule(n.mempoolTTL, func() error { return n.evict(id) }); err != nil {
		n.pool.Remove(id)
		return err
	}
	n.record(network.Event{Event: "mempool_accept", Ref: id, Reason: fmt.Sprintf("pending=%d nonce=%d", n.pool.Len(), tx.Nonce)})
	return n.armBatchWait()
}

// evict drops a transaction whose TTL expired (if it is still pending).
func (n *Runtime) evict(id types.Hash) error {
	if !n.pool.Remove(id) {
		return nil
	}
	delete(n.localTX, id)
	n.resetBatchWait()
	n.record(network.Event{Event: "mempool_evict", Ref: id, Reason: fmt.Sprintf("ttl_ms=%d pending=%d", n.mempoolTTL, n.pool.Len())})
	if err := n.armBatchWait(); err != nil {
		return err
	}
	return n.process()
}

// armBatchWait starts the BlockMaxWait timer once transactions are pending.
// When it fires, a proposer may build a block below MinBlockTXs.
func (n *Runtime) armBatchWait() error {
	if n.pool.Len() == 0 || n.batchScheduled || n.batchReady {
		return nil
	}
	n.batchScheduled = true
	epoch := n.batchEpoch
	if err := n.port.Schedule(n.blocks.wait, func() error {
		if epoch != n.batchEpoch {
			return nil
		}
		n.batchScheduled = false
		if n.pool.Len() == 0 {
			return nil
		}
		n.batchReady = true
		n.record(network.Event{Event: "block_wait_elapsed", Reason: fmt.Sprintf("pending=%d", n.pool.Len())})
		return n.process()
	}); err != nil {
		n.batchScheduled = false
		return err
	}
	return nil
}

func (n *Runtime) selectBatch() []types.Transaction {
	return n.pool.Select(n.engine.State(), n.blocks.MaxBlockTXs, n.blocks.MaxBlockBytes)
}

func (n *Runtime) batchEligible(txs []types.Transaction) bool {
	return len(txs) > 0 && (len(txs) >= n.blocks.MinBlockTXs || n.batchReady)
}

func (n *Runtime) resetBatchWait() {
	n.batchEpoch++
	n.batchScheduled = false
	n.batchReady = false
}
