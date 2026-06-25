package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Metrics accumulates counters that the sampler turns into per-second deltas.
//
// The distinction this whole post is about:
//   - throughput: every unit the backend finishes, useful or not.
//   - goodput:    units finished while a client was still waiting (DoneAt <= Deadline).
type Metrics struct {
	offered     atomic.Int64 // work pushed onto the queue (includes retries)
	completions atomic.Int64 // backend finished the heavy work (throughput)
	good        atomic.Int64 // ...and a client was still waiting (goodput)
	userOK      atomic.Int64 // user-level: an operation a client actually got
	userGaveUp  atomic.Int64 // user-level: client exhausted its retries
}

func (m *Metrics) recordOffer() { m.offered.Add(1) }

// recordCompletion is called by the backend after finishing one unit.
func (m *Metrics) recordCompletion(doneAt, deadline time.Time) {
	m.completions.Add(1)
	if !doneAt.After(deadline) {
		m.good.Add(1)
	}
}

func (m *Metrics) recordUserOK()     { m.userOK.Add(1) }
func (m *Metrics) recordUserGaveUp() { m.userGaveUp.Add(1) }

// sample writes one CSV row per bucket: elapsed seconds, offered/s, throughput/s,
// goodput/s, dropped/s, and the instantaneous queue depth. It runs until done
// is closed, then writes a trailer comment with totals.
func sample(w *os.File, q *Queue, m *Metrics, bucket time.Duration, start time.Time, done <-chan struct{}) {
	fmt.Fprintln(w, "t,offered_s,throughput_s,goodput_s,dropped_s,queue_depth")
	var prevOffered, prevComp, prevGood, prevDropped int64
	tick := time.NewTicker(bucket)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			offered := m.offered.Load()
			comp := m.completions.Load()
			good := m.good.Load()
			dropped := q.Dropped()
			per := bucket.Seconds()
			fmt.Fprintf(w, "%.0f,%.1f,%.1f,%.1f,%.1f,%d\n",
				time.Since(start).Seconds(),
				float64(offered-prevOffered)/per,
				float64(comp-prevComp)/per,
				float64(good-prevGood)/per,
				float64(dropped-prevDropped)/per,
				q.Len(),
			)
			prevOffered, prevComp, prevGood, prevDropped = offered, comp, good, dropped
		}
	}
}
