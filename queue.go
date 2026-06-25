package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// Discipline controls the order in which the backend pulls work, and whether
// already-expired work is discarded before the expensive processing step.
type Discipline string

const (
	// FIFO: classic queue. The oldest work is processed first. This is the
	// naive default and the one that collapses under load.
	FIFO Discipline = "fifo"
	// LIFO: newest work first. Under normal load the queue is ~empty so this
	// behaves like FIFO; it only diverges when a backlog forms, which is
	// exactly when serving the freshest (most likely still-wanted) work helps.
	LIFO Discipline = "lifo"
	// Deadline: FIFO order, but any work whose client deadline has already
	// passed is dropped *before* the backend pays the heavy processing cost.
	// This is the "fail fast on dead work" fix. Surprise: it bounds the queue
	// but does NOT restore goodput, because serving oldest-first means the
	// survivor you pick is always milliseconds from expiring — it dies mid-work.
	Deadline Discipline = "deadline"
	// LIFODeadline: serve newest first AND drop already-expired work. This is the
	// synthesis (cf. Facebook's adaptive LIFO + CoDel): bounded queue and high
	// goodput together.
	LIFODeadline Discipline = "lifo-deadline"
)

// servesNewest reports whether the discipline pops from the back (LIFO order).
func (d Discipline) servesNewest() bool { return d == LIFO || d == LIFODeadline }

// dropsExpired reports whether expired work is discarded before processing.
func (d Discipline) dropsExpired() bool { return d == Deadline || d == LIFODeadline }

// Work is one unit handed from the API server to the backend via the queue.
type Work struct {
	OpID     uint64
	Enqueued time.Time
	// Deadline is the instant the waiting client gives up. After this, finishing
	// the work produces throughput but no goodput: nobody is listening anymore.
	Deadline time.Time
}

// Queue is a concurrency-safe queue with a swappable service discipline.
// It uses a head index so FIFO pops don't repeatedly reslice a growing backlog.
type Queue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	items      []Work
	head       int
	discipline Discipline
	closed     bool

	// dropMargin is the backend's processing budget: work is dropped unless it
	// can still finish *within its deadline* after spending this long on it.
	// Equivalently, the backend runs with an effective deadline of
	// (client deadline - dropMargin). With margin 0 it only drops already-dead
	// work — which, surprisingly, doesn't help goodput. Set it to ~the work time
	// (or a bit more) and the backend stops starting jobs it can't finish.
	dropMargin time.Duration

	dropped atomic.Int64 // work discarded because it can't finish in time
}

func NewQueue(d Discipline, dropMargin time.Duration) *Queue {
	q := &Queue{discipline: d, dropMargin: dropMargin}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push appends work and wakes one waiting backend worker.
func (q *Queue) Push(w Work) {
	q.mu.Lock()
	q.items = append(q.items, w)
	q.mu.Unlock()
	q.cond.Signal()
}

// Pop blocks until work is available, then returns the next item according to
// the discipline. It returns ok=false only once the queue is closed and drained.
// For the Deadline discipline, expired work is silently dropped (counted) and
// Pop keeps going until it finds live work or the queue empties.
func (q *Queue) Pop() (Work, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		for q.len() == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.len() == 0 && q.closed {
			return Work{}, false
		}
		w := q.take()
		if q.discipline.dropsExpired() && time.Now().Add(q.dropMargin).After(w.Deadline) {
			// Can't finish before the client gives up — don't even start.
			q.dropped.Add(1)
			continue
		}
		return w, true
	}
}

// take removes and returns one item per the discipline. Caller holds the lock
// and has verified the queue is non-empty.
func (q *Queue) take() Work {
	if q.discipline.servesNewest() {
		w := q.items[len(q.items)-1]
		q.items = q.items[:len(q.items)-1]
		return w
	}
	// FIFO and Deadline both serve from the front.
	w := q.items[q.head]
	q.head++
	// Compact occasionally so an abandoned prefix can be garbage collected.
	if q.head > 1024 && q.head*2 >= len(q.items) {
		n := copy(q.items, q.items[q.head:])
		q.items = q.items[:n]
		q.head = 0
	}
	return w
}

func (q *Queue) len() int { return len(q.items) - q.head }

// Len reports the current backlog size.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.len()
}

// Dropped reports how much work has been discarded as already-expired.
func (q *Queue) Dropped() int64 { return q.dropped.Load() }

// Close drains the queue and wakes all workers so they can exit.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}
