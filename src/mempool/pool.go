// Package mempool keeps bounded, independently validated pending transactions in RAM.
package mempool

import (
	"encoding/json"
	"fmt"
	"sort"

	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/state"
	"blockchain-lab01/src/types"
)

type entry struct {
	tx   types.Transaction
	size int
}

// Bound future nonce gaps independently of the configured pool quotas.
const maxFutureNonceGap = 1024

// Pool is owned by the node event loop; callers must not mutate it concurrently.
type Pool struct {
	entries                                              map[types.Hash]entry
	accountCount                                         map[types.PublicKey]int
	used, slots, queued                                  int
	globalSlots, globalQueue, accountSlots, accountQueue int
	maxBytes, maxTX                                      int
}

// Limits bounds the pool. Each account has AccountSlots protected slots
// (drawn from GlobalSlots) plus up to AccountQueue overflow entries (drawn from
// GlobalQueue). Field names match the flat node/network JSON configuration.
type Limits struct {
	MempoolBytes, MaxTxBytes                             int
	AccountSlots, AccountQueue, GlobalSlots, GlobalQueue int
}

// Validate checks the slot/queue quotas; byte limits are validated by callers
// that know their transport bounds.
func (l Limits) Validate() error {
	if l.AccountSlots < 1 || l.AccountQueue < 1 || l.GlobalSlots < l.AccountSlots || l.GlobalQueue < l.AccountQueue {
		return fmt.Errorf("invalid mempool slot/queue limits")
	}
	return nil
}

func New(l Limits) *Pool {
	return &Pool{entries: make(map[types.Hash]entry), accountCount: make(map[types.PublicKey]int), globalSlots: l.GlobalSlots, globalQueue: l.GlobalQueue, accountSlots: l.AccountSlots, accountQueue: l.AccountQueue, maxBytes: l.MempoolBytes, maxTX: l.MaxTxBytes}
}
func (p *Pool) Len() int { return len(p.entries) }
func (p *Pool) Admit(chain string, s state.State, tx types.Transaction) (types.Hash, error) {
	var zero types.Hash
	id, err := crypt.TransactionID(tx)
	if err != nil {
		return zero, err
	}
	if _, ok := p.entries[id]; ok {
		return id, fmt.Errorf("duplicate transaction")
	}
	if err := state.ValidateTransaction(chain, tx); err != nil {
		return zero, err
	}
	last := s.LastNonce(tx.Sender)
	if tx.Nonce <= last {
		return id, fmt.Errorf("replayed transaction nonce %d, committed %d", tx.Nonce, last)
	}
	if tx.Nonce-last > maxFutureNonceGap {
		return id, fmt.Errorf("nonce too far ahead")
	}
	for _, e := range p.entries {
		if e.tx.Sender == tx.Sender && e.tx.Nonce == tx.Nonce {
			return id, fmt.Errorf("conflicting pending sender nonce")
		}
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return id, err
	}
	accountPending := p.accountCount[tx.Sender]
	if accountPending < p.accountSlots {
		if p.slots >= p.globalSlots {
			return id, fmt.Errorf("global transaction slots exhausted")
		}
	} else {
		if accountPending-p.accountSlots >= p.accountQueue {
			return id, fmt.Errorf("account transaction queue exhausted")
		}
		if p.queued >= p.globalQueue {
			return id, fmt.Errorf("global transaction queue exhausted")
		}
	}
	if len(b) > p.maxTX || p.used+len(b) > p.maxBytes {
		return id, fmt.Errorf("mempool capacity exceeded")
	}
	p.entries[id] = entry{tx.Clone(), len(b)}
	p.used += len(b)
	p.accountCount[tx.Sender]++
	if accountPending < p.accountSlots {
		p.slots++
	} else {
		p.queued++
	}
	return id, nil
}
func (p *Pool) Select(s state.State, count, byteLimit int) []types.Transaction {
	entries := p.sorted()
	nonces := make(map[types.PublicKey]uint64)
	var txs []types.Transaction
	used := len(`{"Transactions":[]}`)
	for _, e := range entries {
		last, ok := nonces[e.tx.Sender]
		if !ok {
			last = s.LastNonce(e.tx.Sender)
		}
		if e.tx.Nonce != last+1 || len(txs) >= count || used+e.size+1 > byteLimit {
			continue
		}
		tx := e.tx.Clone()
		txs = append(txs, tx)
		used += e.size + 1
		nonces[tx.Sender] = tx.Nonce
	}
	return txs
}
func (p *Pool) Prune(s state.State) {
	for id, e := range p.entries {
		if e.tx.Nonce <= s.LastNonce(e.tx.Sender) {
			p.remove(id, e)
		}
	}
}

// Remove evicts one pending transaction, for example when its TTL expires.
func (p *Pool) Remove(id types.Hash) bool {
	e, ok := p.entries[id]
	if !ok {
		return false
	}
	p.remove(id, e)
	return true
}

func (p *Pool) remove(id types.Hash, e entry) {
	accountPending := p.accountCount[e.tx.Sender]
	delete(p.entries, id)
	p.used -= e.size
	if accountPending <= p.accountSlots {
		p.slots--
	} else {
		p.queued--
	}
	p.accountCount[e.tx.Sender]--
	if p.accountCount[e.tx.Sender] == 0 {
		delete(p.accountCount, e.tx.Sender)
	}
}

// Snapshot returns a deterministic deep copy for read-only observation. The
// node owner loop calls it, so inspection cannot race admission or pruning.
func (p *Pool) Snapshot() []types.Transaction {
	entries := p.sorted()
	out := make([]types.Transaction, len(entries))
	for i, e := range entries {
		out[i] = e.tx.Clone()
	}
	return out
}

// sorted lists entries by (sender, nonce): the deterministic order used for
// block selection and inspection.
func (p *Pool) sorted() []entry {
	entries := make([]entry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].tx, entries[j].tx
		if c := types.ComparePublicKey(a.Sender, b.Sender); c != 0 {
			return c < 0
		}
		return a.Nonce < b.Nonce
	})
	return entries
}

func (p *Pool) Contains(id types.Hash) bool { _, ok := p.entries[id]; return ok }
