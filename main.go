// Command goodput simulates the classic clients -> API server -> queue -> backend
// pipeline and measures throughput vs goodput as load spikes.
//
// The backend's raw throughput ceiling never changes. The only knob that decides
// whether the system keeps doing *useful* work under a spike is the queue
// discipline:
//
//	go run . -discipline=fifo     # naive: collapses, goodput -> 0
//	go run . -discipline=lifo     # serve newest first: cheap, mostly recovers
//	go run . -discipline=deadline # drop dead work before processing: clean fix
//
// Output is a CSV on stdout (or -out file): per-second offered load, throughput,
// goodput, dropped, and queue depth.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var (
		disc      = flag.String("discipline", "fifo", "queue discipline: fifo | lifo | deadline")
		workers   = flag.Int("workers", 4, "backend worker count")
		workMs    = flag.Int("work-ms", 50, "heavy work duration per unit (ms)")
		baseline  = flag.Float64("rate", 60, "baseline arrival rate (ops/s)")
		spike     = flag.Float64("spike-rate", 400, "arrival rate during the spike (ops/s)")
		spikeAt   = flag.Int("spike-start", 20, "spike start (s)")
		spikeDur  = flag.Int("spike-dur", 10, "spike duration (s)")
		timeoutMs = flag.Int("timeout-ms", 1000, "client patience per attempt (ms)")
		pollMs    = flag.Int("poll-ms", 50, "client poll interval (ms)")
		attempts  = flag.Int("max-attempts", 50, "client retries before giving up")
		durSec    = flag.Int("duration", 90, "total run length (s)")
		bucketMs  = flag.Int("bucket-ms", 1000, "metrics bucket size (ms)")
		seed      = flag.Int64("seed", 1, "RNG seed for arrivals")
		outPath   = flag.String("out", "", "CSV output path (default stdout)")
		dropMgnMs = flag.Int("drop-margin-ms", -1, "deadline disciplines: drop work that can't finish within this budget (ms); -1 defaults to work-ms")
	)
	flag.Parse()

	// The backend's effective deadline is (client deadline - dropMargin). Default
	// it to the work time: never start a job you can't finish before it expires.
	if *dropMgnMs < 0 {
		*dropMgnMs = *workMs
	}

	d := Discipline(*disc)
	if d != FIFO && d != LIFO && d != Deadline && d != LIFODeadline {
		fmt.Fprintf(os.Stderr, "unknown discipline %q\n", *disc)
		os.Exit(2)
	}

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	capacity := float64(*workers) / (float64(*workMs) / 1000)
	fmt.Fprintf(os.Stderr,
		"discipline=%s workers=%d work=%dms capacity=%.0f ops/s | baseline=%.0f spike=%.0f@%ds for %ds | timeout=%dms drop-margin=%dms\n",
		d, *workers, *workMs, capacity, *baseline, *spike, *spikeAt, *spikeDur, *timeoutMs, *dropMgnMs)

	store := NewStore()
	queue := NewQueue(d, time.Duration(*dropMgnMs)*time.Millisecond)
	metrics := &Metrics{}
	backend := NewBackend(queue, store, metrics, *workers, time.Duration(*workMs)*time.Millisecond)
	clients := NewClients(store, queue, metrics,
		time.Duration(*timeoutMs)*time.Millisecond,
		time.Duration(*pollMs)*time.Millisecond,
		*attempts, *seed)

	rate := Rate{
		Baseline:   *baseline,
		Spike:      *spike,
		SpikeStart: time.Duration(*spikeAt) * time.Second,
		SpikeDur:   time.Duration(*spikeDur) * time.Second,
	}

	// Backend workers run for the whole simulation.
	backendDone := make(chan struct{})
	go func() { backend.Run(); close(backendDone) }()

	// Sampler writes one CSV row per bucket.
	sampleDone := make(chan struct{})
	start := time.Now()
	go sample(out, queue, metrics, time.Duration(*bucketMs)*time.Millisecond, start, sampleDone)

	// Generate load for the fixed observation window. We deliberately do NOT
	// wait for the full backlog to drain afterward: under a collapse the queue
	// can hold hundreds of thousands of dead items that would take an hour to
	// chew through. The observation window is the story; in-flight users are
	// abandoned when we stop.
	wg := clients.Run(rate, time.Duration(*durSec)*time.Second)

	close(sampleDone)
	queue.Close()

	// Brief grace period so a healthy run can finish cleanly; bounded so a
	// collapsed run doesn't hang.
	settled := make(chan struct{})
	go func() { wg.Wait(); close(settled) }()
	select {
	case <-settled:
		<-backendDone
	case <-time.After(2 * time.Second):
	}

	fmt.Fprintf(os.Stderr, "done: offered=%d throughput=%d goodput=%d dropped=%d userOK=%d gaveUp=%d\n",
		metrics.offered.Load(), metrics.completions.Load(), metrics.good.Load(),
		queue.Dropped(), metrics.userOK.Load(), metrics.userGaveUp.Load())
}
