# Blog post outline: "When throughput stays flat and goodput hits zero"

Working title candidates:
- "Throughput Is a Lie: How a Healthy Service Folds Under a Spike"
- "Goodput: the metric your dashboards aren't showing you"
- "The queue that amplified the storm"

## Thesis
A pipeline (clients -> API server -> queue -> backend) can keep its **throughput**
perfectly flat while its **goodput** collapses to zero. The backend never slows
down; it just spends 100% of its capacity finishing work nobody is waiting for
anymore. The culprit is the queue discipline + a client retry feedback loop.

## 1. The setup
- Clients: `Start work` -> operation handle; poll `Get operation` (pending/done/failed).
- API server: in-memory operation store, pushes one unit of work per Start.
- Queue between API server and backend.
- Backend: pulls work, does heavy work (sleep), reports done.
- Define throughput vs goodput precisely.
- Note: we model the network hops as in-process goroutines/channels; dynamics
  are identical, the code is readable.

## 2. Normal load: everything is fine
- Baseline 60 ops/s, capacity 80 ops/s (4 workers x 50ms). ~75% utilization.
- Graph: offered ≈ throughput ≈ goodput, queue depth ~0.

## 3. The spike
- 10s spike to 400 ops/s (5x capacity).
- Queue grows. Latency crosses the client's 1s patience.
- The KEY mechanic: a timed-out client doesn't just re-poll — it **gives up and
  issues a NEW Start**. The abandoned op is still Pending in the store forever.
- Retries amplify: offered load climbs ABOVE the spike rate (measured ~1100/s).

## 4. The collapse (and why it's metastable)
- FIFO graph: throughput pinned at 80, goodput 0, queue -> hundreds of thousands.
- The spike ends at t=30 but the system never recovers. This is a *metastable
  failure* (Bronson et al., HotOS '21): trigger = spike, sustaining loop = retries.
- Backend is busy doing useless work on the oldest (already-dead) items, so fresh
  work waits behind a wall of corpses -> everyone times out -> everyone retries.
- The punchline sentence: throughput never changed; goodput is the casualty.

## 5. Fix 1 — FIFO -> LIFO (the one-line change)
- Insight: under normal load the queue is ~empty, so LIFO ≡ FIFO. It only
  behaves differently when a backlog exists — exactly when it matters.
- Newest work is the most likely to still be wanted.
- MEASURED: goodput = throughput = 80/s through the entire spike. userOK 6932 vs
  FIFO's 1322. The backlog still grows (old items pile up) but every item the
  backend touches is fresh enough to finish in time.
- Honest caveat: LIFO starves the oldest items (unfairness) and the queue/memory
  still grows unbounded — it doesn't shed the dead work, it just stops serving it.

## 6. Fix 2 — Deadline-aware drop... and a plot twist
- Tag work with the client deadline; backend discards expired work before paying
  the heavy cost. Intuition says this restores goodput. It DOESN'T.
- MEASURED (deadline = FIFO order + drop expired): queue bounded (~5k vs 270k!)
  but goodput STILL 0. Why: serving oldest-survivor-first means the live item you
  pick has ~0ms of headroom and expires *during* the 50ms of work. You drop the
  corpses but then waste capacity on the dying.
- Lesson: dropping dead work fixes the *memory explosion* (real value!) but not
  goodput. Ordering still matters.

## 6b. Synthesis — LIFO + deadline drop
- Serve newest first AND drop expired. MEASURED: bounded queue AND goodput=80/s.
- This is the production answer.

## 7. The production answer & further reading
- Facebook "Fail at Scale" (ACM Queue): adaptive LIFO + CoDel — FIFO normally,
  flip to LIFO + drop-old once queue wait crosses a threshold. Our 6b IS this.
- CoDel / bufferbloat (Nichols & Jacobson): same disease at the network layer.
- Root-cause fixes beyond the queue: bounded queue + load shedding (503 fast),
  client retry budgets + backoff/jitter, circuit breakers.
- Closing: measure goodput, not just throughput. A flat throughput line can hide
  a dead system.

## Repro
- `go run . -discipline=fifo|lifo|deadline -out=data.csv`
- Plotting script -> SVGs.
