# Exercise 2: Catch It in the Act — the Flight Recorder (~25 min)

## Symptom
A long-running lookup service. Eight workers, an in-memory cache, sub-ms
requests, p99 SLO of 50ms. And an on-call report that reads: *"a couple of
times an hour a burst of requests takes 250ms+. Can't reproduce it. By the
time we see it on the dashboard, it's over."*

Run it (takes 12 seconds — a compressed stand-in for hours of production):

```bash
go run main.go
```

```text
2902 requests | avg    200µs | max   2.09ms
2868 requests | avg    210µs | max    910µs
...
SLOW request: 267.811875ms (threshold 100ms)
SLOW request: 270.927667ms (threshold 100ms)   <- 8 of these, then...
2139 requests | avg    1.2ms | max 269.45ms    <- ...back to normal
```

You could wrap the whole run in `trace.Start` — but pretend this is a real
service: hours of uptime at MB/s of trace data. You need the trace of *the
five seconds around the spike*, captured *after* you notice the spike
happened. That's `runtime/trace.FlightRecorder` (official API since Go 1.25 —
if you find `golang.org/x/exp/trace.NewFlightRecorder` in a blog post, that's
the pre-1.25 experiment; don't use it).

The flight recorder keeps a moving window of the most recent trace data in an
in-memory ring buffer. It costs ~1–2% CPU, writes nothing to disk, and on
demand dumps the window — i.e. *the recent past* — to a file.

## Your Task
1. **TODO 1** in `main.go`: create and start a flight recorder.
   ```go
   fr = trace.NewFlightRecorder(trace.FlightRecorderConfig{
       MinAge:   5 * time.Second, // keep >= the last 5s of trace...
       MaxBytes: 16 << 20,        // ...but never more than ~16 MiB
   })
   if err := fr.Start(); err != nil { ... }
   ```
   Knob guidance from the Go blog: set `MinAge` to ~2× the window you'll want
   to look at; expect busy services to produce roughly 2–10 MB/s of trace, so
   size `MaxBytes` accordingly (it wins over `MinAge`; both are hints).
2. **TODO 2**: in the worker's slow-request branch, snapshot the recorder to
   `flightrecorder.trace` — **exactly once** (`sync.Once`), in a fresh
   goroutine so the worker isn't stalled, and guarded by `fr.Enabled()`.
   This detect-then-`WriteTo` pattern is the whole point: the trigger fires
   *after* the interesting behavior, and the buffer still contains it.
3. Run it. When `wrote flight recorder snapshot` appears, open it:
   ```bash
   go tool trace flightrecorder.trace
   ```
4. Explain the spike. Not "requests got slow" — *which goroutine did what to
   whom*.

## What You Should See in the Snapshot
- The snapshot covers only the last ~5s of the run — you shipped a few
  hundred KB instead of a whole-run trace, and it contains the incident.
- **View trace by proc**: seconds of normal request confetti, then a ~270ms
  stretch where the request rows go quiet and one goroutine runs
  uninterrupted. Click it: `main.(*service).refresher`, inside a
  `refresh cache` region.
- **Goroutine analysis**: `main.(*service).worker` (Count 8) — with a large
  **Block time (sync)**; `main.(*service).refresher` with ~270ms of
  execution.
- **Synchronization blocking profile**: ~2s of cumulative delay (8 workers ×
  ~270ms) in `sync.(*RWMutex).RLock` under `main.(*service).handle`. There's
  your convoy: the refresher holds the write lock while it "revalidates",
  and all eight workers pile up behind `RLock`.
- The vague symptom ("max latency 269ms, sometimes") is now a precise
  mechanism: **every fourth cache refresh does a full rebuild while holding
  the write lock**.

<details>
<summary>Solution (TODO 1 + TODO 2)</summary>

```go
// Package scope:
var (
	fr           *trace.FlightRecorder
	snapshotOnce sync.Once
)

// In main, before the workers start:
fr = trace.NewFlightRecorder(trace.FlightRecorderConfig{
	MinAge:   5 * time.Second,
	MaxBytes: 16 << 20,
})
if err := fr.Start(); err != nil {
	log.Fatal(err)
}
defer fr.Stop()

// In worker, replacing the TODO 2 comment:
if elapsed > slowThreshold && fr.Enabled() {
	log.Printf("SLOW request: %v (threshold %v)", elapsed, slowThreshold)
	go snapshotOnce.Do(func() {
		f, err := os.Create("flightrecorder.trace")
		if err != nil {
			log.Printf("creating snapshot file: %v", err)
			return
		}
		defer f.Close()
		if _, err := fr.WriteTo(f); err != nil {
			log.Printf("writing snapshot: %v", err)
			return
		}
		fr.Stop()
		log.Printf("wrote flight recorder snapshot to flightrecorder.trace")
	})
}
```

(add `"os"` to the imports; note the condition change on the `if`)

Details that matter in production, all from the
[Go blog's flight recorder post](https://go.dev/blog/flight-recorder):
- `sync.Once`: eight workers hit the threshold within microseconds of each
  other during a convoy; you want one snapshot, not eight.
- The extra goroutine: `WriteTo` does real work; don't add it to a request's
  latency.
- `fr.Enabled()`: cheap guard so the slow path stays quiet after you've
  stopped the recorder.
- Only one flight recorder may be active per process, but it *can* run
  concurrently with `trace.Start` and the `/debug/pprof/trace` endpoint.
</details>

<details>
<summary>Bonus: actually fix the convoy</summary>

The snapshot tells you the fix too: build the new cache *outside* the
critical section and swap it in under a brief write lock:

```go
// Full revalidation: rebuild and verify OUTSIDE the lock...
next := make(map[int]string, cacheEntries)
for i := range cacheEntries {
	next[i] = fmt.Sprintf("value-%d-%d", n, i)
}
spin(250 * time.Millisecond)
// ...and only lock for the pointer swap.
s.mu.Lock()
s.cache = next
s.mu.Unlock()
```

(This requires moving the locking out of `refresh` — the incremental path
still wants the lock during its writes.) Re-run with your recorder still in
place: no SLOW lines, no snapshot fires. Absence of the trigger is now
evidence of the fix.
</details>

## What This Teaches
Rare events are the tracer's hardest problem — you never have a trace running
when they happen. The flight recorder inverts the game: trace *always*, keep
*almost nothing*, and let the anomaly itself decide when to persist the
evidence. A snapshot like this, attached to an incident ticket, is ground
truth that a teammate — or an AI agent doing the follow-up — can reason from
without access to the running system.
