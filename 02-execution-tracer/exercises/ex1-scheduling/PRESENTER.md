# Presenter: The Heartbeat That Missed

> Presenter-only. Students use README.md.

## Goal
Teach students to recognize **scheduler starvation** in the trace viewer: a
goroutine that misses its deadline not because it's slow but because it's
`Runnable` and nobody will run it. This is the state the trace viewer surfaces
and CPU profiles cannot.

## Reproduce
From the exercise directory:

```bash
cd 02-execution-tracer/exercises/ex1-scheduling
go run main.go
```

Expected output (varies wildly between runs — that's the point):

```text
processed 1000 batches in 3.1s
heartbeat interval: target 10ms | p50 11ms | p99 1.314s | max 1.314s
```

Run it 3–4 times so the room *sees* the variance: p99 will bounce between
~60ms and >2s. p50 always looks fine. Save an ugly one:

```bash
cp scheduling.trace broken.trace
go tool trace broken.trace
```

Trace-viewer cues you're about to walk through:
- Landing page → **Goroutine analysis** shows `sync.(*WaitGroup).Go.func1`
  with **Count 1000**.
- **View trace by proc**: every P row is a solid wall of batch slices.
- **Goroutine analysis → main.heartbeat**: a huge **Sched wait time** column.
- **Scheduler latency profile** dominated by `runtime.asyncPreempt` under
  `main.processBatch`.

## Root cause
The program spawns `numBatches = 1000` goroutines, one per batch, and each
does ~3ms of pure CPU. Spawning is cheap; being *runnable* is not. The Go
scheduler is fair — every runnable G takes turns on a P with ~10ms preemption
quanta. When the heartbeat's timer fires, its goroutine goes to the *back* of
a run queue with ~1000 CPU-hungry Gs in front of it. It only needs
microseconds of CPU, but it can wait hundreds of milliseconds — or seconds —
to get any. The heartbeat isn't slow; it isn't running.

## Walkthrough
Drive this live. Every step names what to click and what to point at.

1. **Show the source first (~30 seconds).** Open `main.go`, scroll to the
   `wg.Go(func() { processBatch(i) })` loop at line 52. That's the whole
   bug in one line: fan-out proportional to workload, not to hardware.

2. **Run twice on stage.** Emphasize that p50 lies:
   ```bash
   go run main.go
   go run main.go
   ```
   If the second run has a mild p99, keep going until you get an ugly one.
   Save it: `cp scheduling.trace broken.trace`.

3. **Open the trace.**
   ```bash
   go tool trace broken.trace
   ```

4. **Landing page → Goroutine analysis.** First eye-catcher: the
   `sync.(*WaitGroup).Go.func1` row with **Count 1000**. Say aloud: "That
   number is the bug." Everything else follows from it.

5. **Click `main.heartbeat` in that list.** The per-group breakdown is the
   money shot. Point at the columns in order:
   - Execution time: ~hundreds of µs. It barely runs.
   - Block time (select): ~1s. Legitimate — waiting for `<-ticker.C`.
   - **Sched wait time: often >1s, sometimes >2s.** Say the definition
     aloud: *"Runnable but not running. The scheduler had work for it, no P
     would take it."*
   This one number is what CPU profilers can't see and this exercise
   exists to teach.

6. **Back to the landing page → View trace by proc.** The wall of blue.
   Every P is saturated. Zoom in with `w` on any spot: thousands of tiny
   slices, ~10ms each, as the scheduler round-robins 1000 Gs.

7. **Find a heartbeat slice.** Hard to see by eye — filter the goroutines
   panel or use the Goroutine analysis page's link to the specific G.
   Measure the gap between two consecutive heartbeat slices with the
   viewer's ruler. That gap is exactly the missed-tick latency.

8. **Scheduler latency profile.** From the landing page, click
   **Scheduler latency profile**, or headless:
   ```bash
   go tool trace -pprof=sched broken.trace > sched.pprof
   go tool pprof -top sched.pprof
   ```
   Expect hundreds of cumulative seconds of scheduler delay concentrated
   under `runtime.asyncPreempt` in `main.processBatch`. The pprof view is
   useful when Chrome isn't available.

9. **Name the diagnosis on the board.** *"Scheduler starvation caused by
   unbounded fan-out. The concurrent unit shouldn't be 'per batch'; it
   should be 'per processor'."*

## Fix
Replace the goroutine-per-batch fan-out with a bounded worker pool sized to
`GOMAXPROCS`:

```go
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

Add `"runtime"` to the imports.

Verify:

```bash
go run main.go
go tool trace scheduling.trace   # the "fixed" trace
```

Expected output:

```text
processed 1000 batches in 3.2s
heartbeat interval: target 10ms | p50 10ms | p99 28ms | max ~40ms
```

Before/after in the viewer — open both `broken.trace` and `scheduling.trace`
in two tabs:
- **Goroutine analysis** front page: `sync.(*WaitGroup).Go.func1` count drops
  from **1000** to ~**8–10**.
- **main.heartbeat** row: Sched wait time collapses from ~1s to
  milliseconds. Show the two side-by-side.
- **View trace by proc**: same wall of blue on the fixed trace (throughput
  is the same!), but heartbeat slices now appear on a regular ~10ms cadence
  because there's always a P free within one preemption quantum.
- **Scheduler latency profile**: the tall `asyncPreempt` bar under
  `processBatch` is gone.

Mention the `GOMAXPROCS-1` variant from the README table: 7% throughput cost
buys the heartbeat ~15ms p99. That's a *knob you can now justify* with two
trace screenshots.

## Ask the room
- What in the trace told you the heartbeat wasn't slow, it wasn't *running*?
  (Answer: `Sched wait time` and the Runnable state — the tracer's whole
  differentiator.)
- Why does adding more goroutines make the tail worse, not better, here?
  (Fair scheduling: every runnable G shares a P; latency-sensitive Gs pay
  the same queue tax as CPU-hungry ones.)
- Where else in your code do you have "one goroutine per work item"? What
  size is the work-item queue at peak?
- The fix leaves throughput unchanged. What does that tell you about the
  cost of goroutines vs. the cost of contention for CPUs?

## Common pitfalls
- **Don't skip running the program multiple times.** The bug is
  probabilistic; a single lucky run can undersell it. Have a pre-generated
  `broken.trace` in your back pocket in case the room's laptop is fast
  enough to hide it.
- **Students often "fix" the symptom, not the cause.** A `runtime.Gosched()`
  in `processBatch` or bumping the heartbeat priority via `time.Sleep`
  tweaks patches the specific case; it doesn't address the queue tax. Push
  them to the worker pool.
- **The scheduler latency profile in a browser is slow to load** for a big
  trace; if the wifi/laptop is stressed, use the headless
  `-pprof=sched` invocation. It renders in a terminal in a second.
- Chromium tab is the only place **View trace by proc** works. Have Chrome
  open before you start; Safari renders a broken timeline.
