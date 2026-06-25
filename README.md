# goodput

A small simulation behind the blog post **"Shed your load: how a healthy service
folds under a spike"** ([index.md](index.md)).

It models the classic asynchronous pipeline —

```
clients ──▶ API server ──▶ queue ──▶ backend
                │                        │
                └──────── store ◀────────┘
```

— and shows how a service whose **throughput never changes** can have its
**goodput** (useful work completed while a client is still waiting) collapse to
zero under a load spike, and how the queue discipline decides whether that
happens.

No third-party dependencies. Go 1.21+.

## Quick start

```sh
# The collapse: FIFO queue, throughput flat, goodput -> 0, never recovers.
go run . -discipline=fifo

# Fix 1 — LIFO: serve newest first. Goodput recovers; queue still grows.
go run . -discipline=lifo

# Fix 2 — naive deadline drop: discard already-expired work.
#         Bounds the queue but goodput stays at 0 (the plot twist).
go run . -discipline=deadline -drop-margin-ms=0

# Fix 3 — backend deadline < client deadline: drop work that can't finish
#         in time. Bounded queue AND full goodput.
go run . -discipline=deadline -drop-margin-ms=70
```

Each run prints a per-second CSV to stdout (or `-out file.csv`) and a summary
line to stderr:

```
discipline=fifo workers=4 work=50ms capacity=80 ops/s | baseline=60 spike=400@20s for 10s | timeout=1000ms drop-margin=50ms
done: offered=286165 throughput=6935 goodput=1322 dropped=0 userOK=1322 gaveUp=4161
```

## Regenerate the charts

Write one CSV per discipline, then render the SVGs used in the post:

```sh
go run . -discipline=fifo                       -out=data_fifo.csv
go run . -discipline=lifo                       -out=data_lifo.csv
go run . -discipline=deadline -drop-margin-ms=0 -out=data_deadline_naive.csv
go run . -discipline=deadline -drop-margin-ms=70 -out=data_deadline.csv
go run ./plot
```

`plot` reads those four CSVs and writes, per discipline:

- `chart_<d>.svg` — throughput vs goodput (the hero chart)
- `chart_<d>_offered.svg` — offered load, including retry amplification
- `chart_<d>_queue.svg` — queue depth over time

plus two comparison charts:

- `chart_goodput_compare.svg` — goodput of all four disciplines on one axis
- `chart_queue_compare.svg` — queue depth of all four on one axis

## Margin sensitivity sweep

`-drop-margin-ms=70` for a 50 ms work time is not magic — it just needs to clear
the work time with a little headroom for scheduling jitter. To see how little the
exact value matters, sweep it:

```sh
go run ./sweep                    # default margins: 0 30 50 55 60 70 100 150 250
go run ./sweep 0 40 50 60 80 120  # or your own (ms)
go run ./plot                     # renders chart_margin_sweep.svg from the result
```

`sweep` runs the `deadline` discipline once per margin (real-time runs, so it
takes a few minutes) and writes `margin_sweep.csv`:

```
margin_ms,goodput,throughput,goodput_pct,dropped,gaveup
```

`plot` then renders `chart_margin_sweep.svg` — goodput as a percentage of
throughput against the margin, with the work time marked. The takeaway: goodput is
near zero until the margin clears the work time, then plateaus near 100% across a
wide range above it. The headline `70` is just a comfortable point on that plateau.

## Flags

| flag | default | meaning |
|---|---|---|
| `-discipline` | `fifo` | `fifo`, `lifo`, `deadline`, or `lifo-deadline` |
| `-drop-margin-ms` | `-1` (= `work-ms`) | deadline disciplines: drop work that can't finish within this budget. `0` = drop only already-expired work. |
| `-workers` | `4` | backend worker count |
| `-work-ms` | `50` | heavy-work duration per unit |
| `-rate` | `60` | baseline arrival rate (ops/s) |
| `-spike-rate` | `400` | arrival rate during the spike (ops/s) |
| `-spike-start` | `20` | spike start (s) |
| `-spike-dur` | `10` | spike duration (s) |
| `-timeout-ms` | `1000` | client patience per attempt |
| `-poll-ms` | `50` | client poll interval |
| `-max-attempts` | `50` | client retries before giving up |
| `-duration` | `90` | total run length (s) |
| `-bucket-ms` | `1000` | metrics bucket size |
| `-seed` | `1` | RNG seed for arrivals |
| `-out` | stdout | CSV output path |

Capacity is `workers / work-ms` — with the defaults, **80 ops/s**. The baseline
(60) sits below it; the spike (400) is 5× over it.

## CSV columns

```
t,offered_s,throughput_s,goodput_s,dropped_s,queue_depth
```

| column | meaning |
|---|---|
| `t` | elapsed seconds |
| `offered_s` | work pushed onto the queue per second (includes retries) |
| `throughput_s` | units the backend finished per second (useful or not) |
| `goodput_s` | units finished while a client was still waiting (`DoneAt <= Deadline`) |
| `dropped_s` | units discarded as un-finishable per second (deadline disciplines) |
| `queue_depth` | backlog size sampled at the end of the bucket |

## How it maps to the post

| concept | code |
|---|---|
| `Start work` / `Get operation` / in-memory store | [`store.go`](store.go) |
| swappable queue discipline (FIFO / LIFO / deadline drop) | [`queue.go`](queue.go) |
| backend worker pool doing heavy work | [`backend.go`](backend.go) |
| clients that time out and **retry** (the feedback loop) | [`client.go`](client.go) |
| throughput vs goodput accounting | [`metrics.go`](metrics.go) |
| flags, wiring, run loop | [`main.go`](main.go) |
| zero-dependency SVG charts | [`plot/plot.go`](plot/plot.go) |

## Notes on the model

- The network hops are collapsed into in-process goroutines and a shared,
  `sync.Cond`-guarded queue. The dynamics (head-of-line blocking, retry
  amplification, metastable collapse) don't depend on the transport.
- A run generates load for `-duration` seconds and then stops; it deliberately
  does **not** wait for a collapsed backlog to drain, which under FIFO would take
  the better part of an hour. The observation window is the story.
- `lifo-deadline` exists but is a trap worth understanding: under LIFO the
  expired items sit at the *bottom* of the stack and are never popped, so
  drop-on-pop never fires and the queue is unbounded — same as plain LIFO. A real
  combined discipline (à la adaptive LIFO + CoDel) must also evict from the old
  end. See the post's "This is a known shape" section.
```
