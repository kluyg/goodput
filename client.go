package main

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Clients models a population of impatient users hitting the API server.
//
// Each user wants one unit of work done. A user calls "Start work", then polls
// "Get operation". If the result hasn't arrived by its patience timeout, the
// user GIVES UP and starts over with a brand-new operation. That retry is the
// load multiplier: under a backlog, every timed-out user injects fresh work,
// which is what turns a transient spike into a self-sustaining (metastable)
// collapse.
type Clients struct {
	store   *Store
	queue   *Queue
	metrics *Metrics

	timeout      time.Duration // user patience for a single attempt
	pollInterval time.Duration // how often a waiting user checks Get()
	maxAttempts  int           // how many times a user retries before giving up
	rng          *rand.Rand
}

// Rate is a piecewise-constant arrival rate: baseline ops/s, jumping to spike
// ops/s for the window [spikeStart, spikeStart+spikeDur).
type Rate struct {
	Baseline   float64
	Spike      float64
	SpikeStart time.Duration
	SpikeDur   time.Duration
}

func (r Rate) at(elapsed time.Duration) float64 {
	if elapsed >= r.SpikeStart && elapsed < r.SpikeStart+r.SpikeDur {
		return r.Spike
	}
	return r.Baseline
}

func NewClients(store *Store, q *Queue, m *Metrics, timeout, poll time.Duration, maxAttempts int, seed int64) *Clients {
	return &Clients{
		store: store, queue: q, metrics: m,
		timeout: timeout, pollInterval: poll, maxAttempts: maxAttempts,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Run generates user arrivals as a Poisson process whose rate follows the spike
// profile, spawning a goroutine per user. It returns once the run duration has
// elapsed; in-flight users are waited on by the caller via the returned WaitGroup.
func (c *Clients) Run(rate Rate, duration time.Duration) *sync.WaitGroup {
	var wg sync.WaitGroup
	start := time.Now()
	for {
		elapsed := time.Since(start)
		if elapsed >= duration {
			break
		}
		lambda := rate.at(elapsed)
		// Exponential inter-arrival time for a Poisson process.
		gap := time.Duration(-math.Log(1-c.rng.Float64()) / lambda * float64(time.Second))
		time.Sleep(gap)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.user()
		}()
	}
	return &wg
}

// user is one person's session: try, wait, give up, retry — until success or
// patience runs out entirely.
func (c *Clients) user() {
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		now := time.Now()
		deadline := now.Add(c.timeout)
		id, work := c.store.Start(now, deadline)
		c.metrics.recordOffer()
		c.queue.Push(work)

		if c.waitForResult(id, deadline) {
			c.metrics.recordUserOK()
			return
		}
		// Timed out. The old operation is abandoned (still Pending in the store);
		// loop around and issue a fresh one.
	}
	c.metrics.recordUserGaveUp()
}

// waitForResult polls Get(id) until the operation is Done or the deadline passes.
func (c *Clients) waitForResult(id uint64, deadline time.Time) bool {
	for {
		if c.store.Get(id) == Done {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(c.pollInterval)
	}
}
