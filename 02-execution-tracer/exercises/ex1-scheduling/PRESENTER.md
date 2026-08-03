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
   `wg.Go(func() { processBatch(i) })` loop at lines 52–56. That's the whole
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

The whole fix is in `solution.diff`, applied from this directory:

```bash
git apply solution.diff        # bounded worker pool sized to GOMAXPROCS, exactly as above
```

> Undo it with `git apply -R solution.diff` when you want the unbounded
> goroutine-per-batch version back for the next session.

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
- **Scheduler latency profile**: the `asyncPreempt` delay collapses from
  ~800s to ~14s. Don't say "gone" — the profile is still dominated by a tall
  bar, it's just `chanrecv2` now (workers idling on the jobs channel), which
  is exactly what healthy looks like.

Mention the `GOMAXPROCS-1` variant from the README table: a few percent of
throughput buys the heartbeat ~15ms p99. That's a *knob you can now justify* with two
trace screenshots.

## Ask the room
Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- What in the trace told you the heartbeat wasn't slow, it wasn't *running*?
  (Answer: `Sched wait time` and the Runnable state — the tracer's whole
  differentiator.) A CPU profile only samples time spent *executing* — if a
  goroutine never gets a P, it never shows up as a hot function, it just
  doesn't show up at all. That's why the logs and a profiler both point at
  `processBatch` and nothing points at `heartbeat`. The trace viewer tracks a
  goroutine's *state*, not just its execution: `main.heartbeat`'s
  Goroutine analysis row splits time into Execution (~hundreds of µs — it
  barely does any work), Block time on `<-ticker.C` (~1s — legitimate,
  expected), and Sched wait time (often >1s). Sched wait time is time spent
  `Runnable` — the goroutine has work to do and is sitting in a run queue,
  not blocked on anything, just waiting for a P to pick it up. That third
  bucket is the tell: the heartbeat isn't slow to execute and it isn't
  stuck waiting on I/O or a channel, it's ready and nobody will run it. No
  profiler built around "where did the CPU go while things were running"
  can see a goroutine that was never running in the first place.

- Why does adding more goroutines make the tail worse, not better, here?
  (Fair scheduling: every runnable G shares a P; latency-sensitive Gs pay
  the same queue tax as CPU-hungry ones.) Go's scheduler doesn't know or
  care that `heartbeat` is latency-sensitive and `processBatch` isn't —
  every `Runnable` G is just an entry in a run queue, and the scheduler
  round-robins them fairly across however many P's `GOMAXPROCS` gives it,
  preempting each one after roughly a 10ms quantum. With 1000
  `processBatch` goroutines and 8 P's, that's ~125 CPU-bound Gs queued
  behind each P at the worst case. When the heartbeat's ticker fires and it
  transitions to `Runnable`, it doesn't jump the line — it goes to the back
  of whichever queue it lands on, behind however many batch goroutines
  happen to be ahead of it. More goroutines means a longer queue means a
  longer wait for the *same* fair-share turn, even though the heartbeat
  only needs microseconds once it actually gets a P. Fairness at the
  goroutine level is exactly what produces unfairness at the
  request/deadline level when the workload mixes a huge number of
  CPU-bound units with one latency-sensitive one — there's no priority
  lane, so throwing more concurrent work at the same P's only lengthens
  everyone's queue, including the goroutine that can least afford it.

- Where else in your code do you have "one goroutine per work item"? What
  size is the work-item queue at peak? This is the general anti-pattern:
  `go handleRequest(r)` per incoming HTTP request, a goroutine per Kafka
  message, per row in a batch job, per WebSocket frame — anywhere fan-out
  is sized to the *workload* instead of to the *hardware*. It looks fine
  under light load because there's always a free P, and it silently turns
  into exactly this exercise's bug the moment load spikes: thousands of
  runnable goroutines queued behind a fixed number of P's, and anything
  latency-sensitive sharing those P's (a heartbeat, a health check, a
  request deadline, GC-sensitive work) pays a queue tax nobody budgeted
  for. The fix is the same shape every time — bound the number of
  concurrent workers to something tied to actual capacity (`GOMAXPROCS` for
  CPU-bound work, a pool sized to a downstream connection limit for
  I/O-bound work) and feed them from a queue/channel, so the number of
  runnable Gs competing for a P is a constant you chose, not a function of
  how much work showed up. Push the room to actually go look: grep their
  own services for `go func` inside a request handler or a consumer loop
  with no semaphore or worker pool around it, and ask what the queue depth
  looks like at their real peak load, not their test load.

- The fix leaves throughput unchanged. What does that tell you about the
  cost of goroutines vs. the cost of contention for CPUs? It isolates where
  the cost actually lives. Spawning 1000 goroutines vs. 8 doesn't move
  total wall-clock time for the batch work at all — same "wall of blue" on
  the by-proc view, same ~3.1–3.2s either way — because a goroutine's own
  overhead (a few KB of stack, a scheduler struct) is cheap and mostly
  irrelevant once you have enough of them to keep every P busy. What's
  expensive isn't the goroutines existing, it's *contention for the fixed
  number of P's/OS threads* that actually run code — and that contention
  cost doesn't show up as slower total throughput, it shows up as queueing
  delay distributed unfairly across whichever goroutines happen to be
  waiting, which is invisible to a throughput number and devastating to a
  tail-latency number. The worker-pool fix doesn't reduce the total amount
  of CPU work Go has to schedule; it just stops manufacturing thousands of
  redundant `Runnable` entries that all have to take a turn before the one
  that matters gets to run. That's the reframe worth landing: "more
  goroutines" was never buying anything here — the CPU budget was already
  saturated at 8 P's — it was only making the scheduler's fairness policy
  work against the goroutine that needed to jump the queue.

## Common pitfalls
- **Don't skip running the program multiple times.** The bug is
  probabilistic; a single lucky run can undersell it. Have a pre-generated
  `broken.trace` in your back pocket in case the room's laptop is fast
  enough to hide it.
- **Occasionally the runtime writes a trace nothing can read.** Roughly 1
  run in 25 under heavy machine load produces a `scheduling.trace` that
  fails to parse with `expected batch event, got event 0` or `expected
  stack event, got 4`. It is not a truncated file and not a tool-version
  problem — `go tool trace`, `go tool trace -d=parsed`, and the ex4
  analyzer all fail identically on the same bytes, and the failure is
  deterministic per file. Nothing in the notes can fix it: **just delete
  the trace and re-run.** Worth saying out loud if it happens live, so the
  room doesn't think they broke something.
- **Students often "fix" the symptom, not the cause.** A `runtime.Gosched()`
  in `processBatch` or bumping the heartbeat priority via `time.Sleep`
  tweaks patches the specific case; it doesn't address the queue tax. Push
  them to the worker pool.
- **The scheduler latency profile in a browser is slow to load** for a big
  trace; if the wifi/laptop is stressed, use the headless
  `-pprof=sched` invocation. It renders in a terminal in a second.
- Chromium tab is the only place **View trace by proc** works. Have Chrome
  open before you start; Safari renders a broken timeline.
