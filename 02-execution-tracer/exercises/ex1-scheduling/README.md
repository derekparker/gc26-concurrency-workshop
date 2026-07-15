# Exercise 1: The Heartbeat That Missed (~20 min)

## Symptom
This service crunches a backlog of 1000 analytics batches while a heartbeat
goroutine is supposed to tick every 10ms (miss a few in a row and the
orchestrator kills the instance). Ops says it's "randomly getting restarted
under load". A CPU profile shows exactly what you'd expect, batch work, and
nothing wrong.

Run it a few times:

```bash
go run main.go
```

Typical output:

```text
processed 1000 batches in 3.1s
heartbeat interval: target 10ms | p50 11ms | p99 1.314s | max 1.314s
```

p50 looks fine. p99 is catastrophic, and it varies wildly between runs
(sometimes 60ms, sometimes over 2s). Classic "fast on average, terrible tail":
invisible in logs, invisible in CPU profiles, because the heartbeat goroutine
isn't *doing* anything slowly, it's **not being run at all**.

## Your Task
1. Run the program until you catch an ugly run (p99 ≫ 100ms), and keep that
   trace for later comparison:
   ```bash
   go run main.go        # repeat until p99 is embarrassing
   cp scheduling.trace broken.trace
   ```
2. Open it and find out where the heartbeat's time goes:
   ```bash
   go tool trace broken.trace
   ```
3. Fix the program so the batch work still saturates the CPU but the
   heartbeat's p99 stays close to 10ms.
4. Re-run, trace again, and compare `scheduling.trace` (fixed) against
   `broken.trace` (before), same tool, two tabs.

## What to Look For
- **Goroutine analysis → `main.heartbeat`**: look at the columns. On a bad
  run you'll see something like: Total 3.1s, Execution time ~300µs, Block
  time (select) ~1s, that part is legitimate, it's waiting for the next
  tick, and **Sched wait time ~2.2s**. The goroutine was *runnable*,
  awake, ready, asking for CPU, for two-thirds of its life.
- **View trace by proc**: every processor row is a solid wall of batch
  goroutine slices. Zoom in (`w`): thousands of tiny slices as the scheduler
  round-robins 1000 runnable goroutines. Find a heartbeat slice and measure
  the gap to the previous one.
- **Scheduler latency profile** (or headless:
  `go tool trace -pprof=sched broken.trace > sched.pprof && go tool pprof -top sched.pprof`):
  hundreds of cumulative seconds of scheduler delay, almost all under
  `runtime.asyncPreempt` in `main.processBatch`, everyone is waiting in
  line behind everyone else.
- **Goroutine analysis** front page: `sync.(*WaitGroup).Go.func1`, Count
  **1000**. That number *is* the bug.

<details>
<summary>Hint 1 (if you're stuck on diagnosis)</summary>

The trace viewer's "Runnable" state (and the "Sched wait time" column) means:
this goroutine had work to do and no processor would take it. The heartbeat
only needs microseconds of CPU, but when its timer fires it goes to the back
of a run queue that has ~1000 CPU-hungry goroutines ahead of it, each getting
a ~10ms preemption quantum.
</details>

<details>
<summary>Hint 2 (if you're stuck on the fix)</summary>

Don't spawn one goroutine per batch. Spawning goroutines is cheap; *having
1000 of them runnable at once* is not free, the scheduler is fair, and fair
means your latency-sensitive goroutine waits its turn behind 1000 peers.
Bound the concurrency to roughly the number of processors.
</details>

<details>
<summary>Solution</summary>

Replace the goroutine-per-batch fan-out with a worker pool sized to the
machine:

```go
// Process the backlog with a bounded worker pool.
start := time.Now()
var wg sync.WaitGroup
jobs := make(chan int)
for range runtime.GOMAXPROCS(0) {
	wg.Go(func() {
		for id := range jobs {
			processBatch(id)
		}
	})
}
for i := range numBatches {
	jobs <- i
}
close(jobs)
wg.Wait()
```

(add `"runtime"` to the imports)

Measured on an M-series laptop (your numbers will vary):

| | throughput | heartbeat p99 |
|---|---|---|
| broken (1000 goroutines) | 3.1s | 50ms–2.3s, varies |
| worker pool (`GOMAXPROCS`) | 3.2s | ~28ms |
| worker pool (`GOMAXPROCS-1`) | 3.4s | ~15ms |

Same throughput, tail latency rescued. The `GOMAXPROCS-1` row is the going
further: leaving one processor's worth of headroom buys the heartbeat nearly
perfect latency for ~7% throughput, a real tradeoff you can now *see* and
justify with two trace screenshots.

Verify in the new trace: `main.heartbeat`'s Sched wait time collapses to
milliseconds, and the goroutine count drops from ~1000 to ~10.
</details>

## What This Teaches
"Runnable" is the most underrated state in the trace viewer. CPU profilers
sample *running* code; logs record what *ran*. Only the tracer records the
time between "ready" and "running", and that's where tail latency hides.
