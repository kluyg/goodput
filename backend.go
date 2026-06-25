package main

import (
	"sync"
	"time"
)

// Backend is the pool of workers doing the expensive work. Each worker pulls one
// unit from the queue, sleeps for workTime to simulate heavy CPU/IO, then reports
// the operation done back to the API server's store.
//
// The pool's raw rate (workers / workTime) is its *throughput* ceiling and never
// changes. Whether that throughput is useful — goodput — depends entirely on the
// queue discipline feeding it.
type Backend struct {
	q        *Queue
	store    *Store
	metrics  *Metrics
	workers  int
	workTime time.Duration
}

func NewBackend(q *Queue, store *Store, m *Metrics, workers int, workTime time.Duration) *Backend {
	return &Backend{q: q, store: store, metrics: m, workers: workers, workTime: workTime}
}

// Run starts the worker pool and blocks until the queue is closed and drained.
func (b *Backend) Run() {
	var wg sync.WaitGroup
	for i := 0; i < b.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				w, ok := b.q.Pop()
				if !ok {
					return
				}
				time.Sleep(b.workTime) // heavy work
				done := time.Now()
				b.store.Complete(w.OpID, done)
				b.metrics.recordCompletion(done, w.Deadline)
			}
		}()
	}
	wg.Wait()
}
