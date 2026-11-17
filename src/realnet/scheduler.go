package realnet

import (
	"container/heap"
	"fmt"
	"time"
)

// task is a callback due at a wall-clock time; seq keeps FIFO order for ties.
type task struct {
	at  time.Time
	seq uint64
	run func() error
}
type tasks []task

func (q tasks) Len() int { return len(q) }
func (q tasks) Less(i, j int) bool {
	if q[i].at.Equal(q[j].at) {
		return q[i].seq < q[j].seq
	}
	return q[i].at.Before(q[j].at)
}
func (q tasks) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *tasks) Push(x any)   { *q = append(*q, x.(task)) }
func (q *tasks) Pop() any     { a := *q; x := a[len(a)-1]; *q = a[:len(a)-1]; return x }

// Schedule runs f on the owner loop after delay milliseconds.
func (p *process) Schedule(delay uint64, f func() error) error {
	if delay > uint64((time.Duration(1<<63-1))/time.Millisecond) || f == nil {
		return fmt.Errorf("real timer overflow")
	}
	p.seq++
	heap.Push(&p.queue, task{time.Now().Add(time.Duration(delay) * time.Millisecond), p.seq, f})
	return nil
}

// due pops the earliest task if its time has come.
func (p *process) due() (task, bool) {
	if len(p.queue) > 0 && !p.queue[0].at.After(time.Now()) {
		return heap.Pop(&p.queue).(task), true
	}
	return task{}, false
}

// untilNext is how long the owner loop may block before the next task.
func (p *process) untilNext() time.Duration {
	if len(p.queue) == 0 {
		return time.Hour
	}
	return max(time.Until(p.queue[0].at), 0)
}
