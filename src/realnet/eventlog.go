package realnet

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	cons "blockchain-lab01/src/consensus"
	"blockchain-lab01/src/network"
	"blockchain-lab01/src/types"
)

// logWriter writes events.jsonl. With Trace every event is written in full;
// otherwise a compact line is written for important events only, and noisy
// kinds are rate-limited to one line per key per second.
type logWriter struct {
	mu      sync.Mutex
	file    *os.File
	start   time.Time
	seq     uint64
	err     error
	trace   bool
	console bool
	last    map[string]time.Time
}

// importantEvents are always written in compact (non-trace) mode.
var importantEvents = map[string]bool{
	"node_stop": true, "listen": true, "membership": true, "mempool_accept": true, "mempool_evict": true,
	"transaction_reject": true, "transaction_replay": true, "block_wait_elapsed": true,
	"consensus_step": true, "consensus_proposal": true, "consensus_timeout": true, "consensus_lock": true,
	"consensus_vote_signed": true, "state_commit": true, "proposer_crash": true, "block": true, "unblock": true,
	"reject_handshake": true, "ledger_error": true, "fault_drop": true, "fault_duplicate": true,
	"fault_reorder": true, "fault_replay": true, "queue_full": true, "behavior": true, "behavior_message": true,
}

func isReject(event string) bool { return event == "reject" || event == "consensus_reject" }

// keepCompact decides whether a compact log keeps an event: important kinds,
// plus rejections that are T3/T7 evidence (bad signature/domain, equivocation).
func keepCompact(e network.Event) bool {
	if importantEvents[e.Event] {
		return true
	}
	return isReject(e.Event) && (strings.Contains(e.Reason, "signature") || strings.Contains(e.Reason, "domain") || strings.Contains(e.Reason, "conflicting vote"))
}

// rateKey groups noisy compact events; ok is false for unlimited kinds.
func rateKey(e network.Event) (key string, ok bool) {
	switch {
	case e.Event == "transaction_reject" || isReject(e.Event):
		return e.Event + string(e.Type) + e.Reason, true
	case strings.HasPrefix(e.Event, "fault_") || e.Event == "queue_full":
		return e.Event + string(e.Type), true
	}
	return "", false
}

func (l *logWriter) record(e network.Event) {
	if !l.trace && !keepCompact(e) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return
	}
	if key, limited := rateKey(e); !l.trace && limited {
		if l.last == nil {
			l.last = make(map[string]time.Time)
		}
		now := time.Now()
		if now.Sub(l.last[key]) < time.Second {
			return
		}
		l.last[key] = now
	}
	l.seq++
	e.Seq = l.seq
	e.At = uint64(time.Since(l.start) / time.Millisecond)
	if l.trace {
		l.err = json.NewEncoder(l.file).Encode(struct {
			WallTime string
			network.Event
		}{time.Now().UTC().Format(time.RFC3339Nano), e})
		return
	}
	l.err = json.NewEncoder(l.file).Encode(compactLine(e, time.Now()))
	if l.console {
		fmt.Printf("%s node=%d %-22s height=%d round=%d %s %s\n", time.Now().Format("15:04:05"), e.Node, e.Event, e.Height, e.Round, e.Reason, e.StateRoot)
	}
}

type compactEvent struct {
	Ts       string // UTC with milliseconds; lets Alloy store the event time, not the ingest time.
	Time     string
	Node     int
	Event    string
	Height   uint64  `json:",omitempty"`
	Round    *uint64 `json:",omitempty"`
	Step     string  `json:",omitempty"`
	Proposer *int    `json:",omitempty"`
	Hash     string  `json:",omitempty"`
	Root     string  `json:",omitempty"`
	Detail   string  `json:",omitempty"`
}

func compactLine(e network.Event, now time.Time) compactEvent {
	ref := ""
	if e.Ref != (types.Hash{}) {
		ref = hex.EncodeToString(e.Ref[:])
	}
	line := compactEvent{Ts: now.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Time: now.Format("15:04:05.000"), Node: e.Node, Event: e.Event, Height: e.Height, Hash: ref, Root: e.StateRoot, Detail: e.Reason}
	if e.Round != 0 {
		line.Round = &e.Round
	}
	if strings.HasPrefix(e.Event, "consensus_") {
		// Round 0 and node 0 are real values here, so these are never omitted.
		line.Round, line.Proposer = &e.Round, e.Proposer
		if e.Step != 0 {
			line.Step = cons.Step(e.Step).String()
		}
	}
	return line
}

func (l *logWriter) failure() error { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
