# Exercise 3: The Pipeline That Wasn't Parallel (~20 min)

## Symptom
The nightly record shipper: download 200 records from an internal API (~30ms
each), compress each one, append to an archive. A previous owner parallelized
the downloads across 8 workers and left this note in the code:

> "Should take ~1s now (200 records / 8 workers × 30ms per fetch), but it
> still takes over 5 seconds?! Tried 16 and 32 workers: NO change. The API
> team swears it isn't them. I give up."

Reproduce it:

```bash
go run main.go
```

```text
shipped 200 records in 5.042s (39.7 records/sec)
expected ~750ms with 8 workers... where does the time go?
```

Every standard tool is unhelpful here: a CPU profile blames `compress` (true,
but it's "only" 25ms per record); the machine is ~90% idle; nothing logs slow;
and — the genuinely evil part — **changing the worker count changes nothing**,
which usually sends people hunting in exactly the wrong place (the network,
the API, the kernel). Try it: bump `numWorkers` to 32. Same 5 seconds.

## Your Task
1. Trace it. The program already writes `io.trace` and is annotated with a
   `shipRecords` task and regions (`fetch`, `send result`, `compress`,
   `append to archive`) — this exercise assumes the annotation habit from the
   demo, and the region names will do a lot of work for you.
   ```bash
   go run main.go
   go tool trace io.trace
   ```
2. Find where a record's time actually goes. Then fix the pipeline so the
   wall time gets close to the note-writer's ~1s estimate — and so that
   adding workers helps again.

## What to Look For
- **View trace by proc**: for the first ~250ms, overlapping activity — 8
  fetches in flight. Then the shape changes: **a staircase**. One goroutine
  (`main.collector`) runs continuously, back-to-back 25ms `compress` regions,
  while everything else lies idle in long gaps.
- **Goroutine analysis** front page — this one table is the whole diagnosis:

  | Start location | Count | Total execution time |
  |---|---|---|
  | `main.collector` | 1 | **5.0s** |
  | `main.worker` | 8 | 7.9ms |

  The "8-way parallel" pipeline spends 99.8% of its execution time in one
  goroutine.
- **`main.worker` group**: the dominant column is **Block time (chan send)**.
  The workers fetch for 30ms, then queue for *seconds* to hand off their
  result. Their `send result` regions dwarf their `fetch` regions.
- **Synchronization blocking profile**: ~38s of cumulative delay in
  `runtime.chansend1` under `main.worker`. In the same profile, everything
  under `main.fetch` (the actual downloads) totals ~6s across all workers —
  the API team was right.
- **User-defined regions** (`/userregions`): `fetch` ×200 at ~30ms,
  `compress` ×200 at ~25ms, and `send result` ×200 with a horrifying
  distribution. Serialization, quantified.

The structure of the bug: `results` is an unbuffered channel feeding a
**single** collector that does 25ms of CPU work per record. Throughput is
capped at 1000ms/25ms = 40 records/sec **no matter how many workers you
add** — concurrency upstream of a serial stage is just a more expensive
queue.

<details>
<summary>Hint (before the full solution)</summary>

Two instincts will occur to you; test both against the trace:
1. *"Buffer the channel!"* — `make(chan record, 200)` lets the workers finish
   fetching early... and the program still takes ~5s, because the collector
   still compresses 200 records one at a time. The buffer moved the queue; it
   didn't remove the serial stage. (Really try this — watching the "fix" fail
   in the trace is the best part of the exercise.)
2. The collector does two jobs: compression (CPU, 25ms, parallelizable) and
   appending to the archive file (order-insensitive here, µs, must be
   single-writer). Which one *needs* to be serial?
</details>

<details>
<summary>Solution</summary>

Move compression into the workers — parallelize the CPU stage, keep only the
file append serial:

```go
// worker: compress before handing off.
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

// collector: now just the single-writer file append.
for rec := range results {
	trace.WithRegion(ctx, "append to archive", func() {
		if _, err := archive.Write(rec.body); err != nil {
			log.Fatal(err)
		}
	})
}
```

Measured results (M-series laptop):

| version | 8 workers | 16 workers |
|---|---|---|
| broken (serial compress in collector) | 5.04s | 5.0s (no change) |
| fixed (compress in workers) | **1.42s** | **0.78s** |

Adding workers helps again — the defining property of the bug is gone. In the
fixed trace: the staircase is replaced by overlapping `compress` regions
across procs, `main.worker` execution time is ~5s *spread across 8
goroutines*, and Block time (chan send) collapses.

Why ~1.4s and not the note's 750ms? Each record now costs a worker
30ms + 25ms, so 200 × 55ms / 8 ≈ 1.4s — the estimate in the code comment
ignored compression. The trace even fixes your coworker's math.
</details>

## What This Teaches
"We made it concurrent" is a claim about code shape; the tracer checks the
claim against reality. Hidden serialization — one mutex, one unbuffered
channel into one consumer, one connection pool of size 1 — is invisible in
logs and profiles but *jumps* out of a timeline: it literally looks like a
staircase. When throughput won't scale with workers, don't tune; trace, and
look for the stage where the parallelism dies.
