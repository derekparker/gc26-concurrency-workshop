# Presenter: The Pipeline That Wasn't Parallel

> Presenter-only. Students use README.md.

## Goal
Teach how to spot **hidden serialization** in a nominally-concurrent
pipeline. The bug: a "8-way parallel" fan-out that funnels into a single
25ms-per-item serial stage. Throughput is capped by that stage, and adding
workers does nothing. On the timeline this bug **looks like a staircase**.

## Reproduce
From the exercise directory:

```bash
cd 02-execution-tracer/exercises/ex3-io
go run main.go
```

Expected output:

```text
shipped 200 records in 5.042s (39.7 records/sec)
expected ~750ms with 8 workers... where does the time go?
```

Ask the room to guess where the time went, then encourage them to try the
obvious tune — bump `numWorkers` to 16 or 32. Same 5 seconds. The
"more workers helps" instinct produces zero improvement. Now trace it:

```bash
go tool trace io.trace
```

Trace-viewer cues you're about to walk through:
- **View trace by proc**: ~250ms of dense overlapping activity at the
  start, then a **staircase** — one goroutine running back-to-back 25ms
  slices, other P rows empty.
- **Goroutine analysis**: `main.collector` × 1 with ~**5.0s** execution
  time; `main.worker` × 8 with **7.9ms** total execution and huge
  **Block time (chan send)**.
- **Synchronization blocking profile**: ~38s cumulative in
  `runtime.chansend1` under `main.worker`.
- **User-defined regions** (`/userregions`): `fetch` × 200 at ~30ms each,
  `compress` × 200 at ~25ms each, `send result` × 200 with a wide
  distribution — most tiny, many *huge*.

## Root cause
`results` is an **unbuffered** channel between 8 workers and **one**
collector. The collector does two things per record — `compress` (25ms of
CPU) and `append to archive` (µs of I/O) — serially, for every record it
receives. Wall-clock throughput is capped at
`1000ms / 25ms = 40 records/sec`, **regardless of how many workers fetch
in parallel upstream**. Concurrency upstream of a serial stage is just a
more expensive queue: workers finish their 30ms fetch, then block for
seconds on `results <- rec` waiting for the collector to consume.

## Walkthrough
Drive this live. Every step points at something concrete in the tool.

1. **Set the trap first.** Read the previous-owner note in `main.go`
   comments aloud: *"Should take ~1s now (200 records / 8 workers × 30ms
   per fetch), but it still takes over 5 seconds?! Tried 16 and 32
   workers: NO change. The API team swears it isn't them. I give up."*
   Ask the room where they'd look first. Common wrong answers to acknowledge
   and discard: the API (it's local `httptest`), the network, GC, the mutex.

2. **Run it as-is.**
   ```bash
   go run main.go
   ```
   Note the reported time: ~5s.

3. **Prove the "add workers" fix fails.** Live-edit `numWorkers` to 32:
   ```go
   numWorkers = 32
   ```
   Run again:
   ```bash
   go run main.go
   ```
   Same ~5s. Change it back to 8. Say aloud: *"When the fix that
   'should' work does nothing, stop tuning. Trace."*

4. **Open the trace.**
   ```bash
   go tool trace io.trace
   ```

5. **Landing page → Goroutine analysis.** This one table is the whole
   diagnosis, don't skip it:

   | Start location | Count | Total execution time |
   |---|---|---|
   | `main.collector` | 1 | **5.0s** |
   | `main.worker` | 8 | 7.9ms |

   Say aloud: *"The 'parallel' pipeline spends 99.8% of its execution
   time in a single goroutine."* That number alone should tell the room
   this isn't a parallelism problem, it's a *shape* problem.

6. **View trace by proc.** Zoom out (`s`) so you see the whole run.
   Point at the shape:
   - First ~250ms: dense overlapping activity across the P rows —
     8 fetches in flight, working as expected.
   - Then it collapses into a **staircase**: one goroutine, running
     back-to-back 25ms slices, on one P at a time. Zoom in (`w`) on a
     stretch of staircase.
   - Click a step: `main.collector` inside a `compress` region.
   - Point at the empty rows: 7 of 8 workers idle, "done fetching" and
     stuck.

7. **Back to the worker group.** Click **main.worker** in Goroutine
   analysis. Point at:
   - Execution time: tiny.
   - **Block time (chan send)**: enormous — this is where the workers
     went. They fetch for 30ms, then wait *seconds* to hand off.

8. **User-defined regions** (`/userregions`). This confirms it beautifully:
   - `fetch` × 200 — bell curve around ~30ms.
   - `compress` × 200 — tight around ~25ms.
   - `send result` × 200 — a distribution from microseconds to seconds.
     Some sends were instant; most waited in a growing queue.

9. **Synchronization blocking profile.** From the landing page, or
   headless:
   ```bash
   go tool trace -pprof=sync io.trace > sync.pprof
   go tool pprof -top sync.pprof
   ```
   Expect ~38s of cumulative delay in `runtime.chansend1` under
   `main.worker`. In the same profile, `main.fetch` sits at ~6s — the
   API team was right, and pprof can prove it.

10. **State the mechanism.** Draw it on the whiteboard or say it clearly:
    *"Concurrency upstream of a serial stage is just a more expensive
    queue. Throughput = 1000ms / 25ms = 40 rec/s, no matter the worker
    count."*

11. **(Optional — highly recommended for the "aha".)** Show the tempting
    wrong fix: buffer the channel.
    ```go
    results := make(chan record, 200)
    ```
    Re-run — still ~5s. The buffer moved the queue; it didn't remove the
    serial stage. **Watching the "fix" fail in the trace is the best part
    of the exercise.** Revert.

## Fix
Move `compress` into the workers. That's the CPU stage — it *can* be
parallel. Keep the file append (order-insensitive, single writer) in the
collector.

```go
// worker: fetch then compress, both concurrent; then hand off.
for id := range jobs {
    var rec record
    trace.WithRegion(ctx, "fetch", func() {
        rec = fetch(apiURL, id)
    })
    trace.WithRegion(ctx, "compress", func() {
        rec.body = compress(rec)
    })
    trace.WithRegion(ctx, "send result", func() {
        results <- rec
    })
}

// collector: just the single-writer file append.
for rec := range results {
    trace.WithRegion(ctx, "append to archive", func() {
        if _, err := archive.Write(rec.body); err != nil {
            log.Fatal(err)
        }
    })
}
```

Note that `record.body` is now the compressed bytes — the `collector` no
longer needs a separate `compressed` variable.

Verify:

```bash
go run main.go
```

Expected: **~1.4s with 8 workers**, **~0.8s with 16**. Adding workers helps
again — the defining property of the bug is gone. Reported target of
~750ms was optimistic because it ignored compression: each record is now
`30ms + 25ms = 55ms` of work per worker.

Before/after in the viewer — open `io.trace` from both runs:
- **Goroutine analysis**: `main.worker` execution time now ~5s
  *spread across 8 goroutines* (≈600ms each). `main.collector` execution
  time drops from 5s to milliseconds (just file writes).
- **View trace by proc**: the staircase is gone. `compress` regions
  overlap across P rows.
- **Block time (chan send)** on `main.worker`: collapses.
- **User regions**: `send result` distribution flattens to microseconds.

## Ask the room
- What in the trace *first* pointed at the collector, not the workers?
  (The `main.collector`: 1 with 5.0s execution time in Goroutine
  analysis.)
- Why did buffering the channel not help? Where did the bottleneck go?
  (Nowhere — it moved from `chan send` blocking to `chan recv` blocking,
  same 40 rec/s ceiling.)
- What's the general shape of "hidden serialization" on the timeline?
  (A staircase: one goroutine running back-to-back slices while others
  sit idle.)
- Which stages in this pipeline are *legitimately* serial and why?
  (`archive.Write` — order-insensitive here, but must be single-writer;
  everything else is safely parallelizable.)

## Common pitfalls
- **Don't skip the "add workers" experiment.** Watching 32 workers make
  no difference is the shock that motivates the trace. It's a 15-second
  detour that sells the whole lesson.
- **Students go for the buffered-channel fix first.** Let them — as
  above, the trace disproves it beautifully. Do the buffered version
  *before* the real fix, not instead of it.
- **The trace viewer's User Regions page can be slow** on this trace
  (~600 region events). Give it a moment; don't refresh.
- **`spin()` uses busy-loop CPU** so the compression looks like real
  work in a proc row. If a student's machine is under thermal pressure,
  numbers wobble but the shape doesn't; anchor on shape.
- **The "why not 750ms" question comes up.** Answer: the code's original
  estimate ignored compression. The trace even fixes your coworker's
  math.
