# Presenter: Catch It in the Act — the Flight Recorder

> Presenter-only. Students use README.md.

## Goal
Teach the flight recorder pattern: **trace always, keep almost nothing,
snapshot on anomaly.** By the end the room can explain a lock convoy from a
sub-megabyte snapshot they wouldn't have known to capture in advance.

This exercise is often delivered as a **guided walkthrough** — the pressure
valve in the middle of the section. Drive it live; give the room the code
in real time.

## Reproduce
From the exercise directory:

```bash
cd 02-execution-tracer/exercises/ex2-flightrecorder
go run main.go
```

Expected output (the program runs for 12s):

```text
2902 requests | avg    200µs | max   2.09ms
2868 requests | avg    210µs | max    910µs
...
SLOW request: 267.811875ms (threshold 100ms)
SLOW request: 270.927667ms (threshold 100ms)
SLOW request: 271.144542ms (threshold 100ms)
... (8 of these in a burst) ...
2139 requests | avg    1.2ms | max 269.45ms
2895 requests | avg    200µs | max   2.14ms   <- back to normal
```

Once the students add TODO 1 + TODO 2, the burst will also produce:

```text
wrote flight recorder snapshot to flightrecorder.trace
```

Then:

```bash
go tool trace flightrecorder.trace
```

Trace-viewer cues:
- **View trace by proc**: seconds of "request confetti" (short spikes), then
  a ~270ms stretch where one goroutine runs uninterrupted and everything
  else goes quiet.
- **Goroutine analysis**: `main.(*service).worker` (Count 8) with a large
  **Block time (sync)**; `main.(*service).refresher` with ~270ms of
  execution.
- **Synchronization blocking profile**: ~2s of cumulative delay in
  `sync.(*RWMutex).RLock` under `main.(*service).handle`.
- The snapshot is a few hundred KB — not a 12-second whole-run trace.

## Root cause
Every fourth cache refresh (`n%4 != 3` in `service.refresh`) is a **full
revalidation**: build a fresh 50 000-entry map, "verify" it with a 250ms
spin — and it does all of this while **holding the write lock**. Meanwhile
eight worker goroutines are in `handle`, blocked in `RLock`. Classic lock
convoy: one G holding an exclusive lock for hundreds of milliseconds behind
which N reader Gs pile up. The dashboard sees "max latency 269ms" once every
~8 seconds; the trace shows exactly which goroutine did what to whom.

## Walkthrough
Because this is a guided walkthrough, drive it as a live-code session.
The room watches you type the two TODOs. Commands are complete; you can copy
them straight from this file to a terminal.

1. **Show `main.go` and read the on-call note (~1 min).**
   Point at:
   - `runFor = 12 * time.Second` — a stand-in for hours of production.
   - `slowThreshold = 100 * time.Millisecond` — the SLO breach threshold.
   - `service.refresh` and the `n%4 != 3` branch: the whole-map rebuild
     while `s.mu.Lock()` is held.
   - The `TODO 1` and `TODO 2` comments.

2. **Run it unmodified.**
   ```bash
   go run main.go
   ```
   Ask the room: *"You just saw the SLOW lines fly by. That was the
   incident. Now try to trace it."* Beat: `trace.Start` for 12s would work
   here — but this is a stand-in for hours of uptime and GB/s of trace.
   You need the trace of *the 5 seconds around the burst*, captured *after*
   you notice it.

3. **Live-code TODO 1.** At package scope, above `type service struct`:

   ```go
   var (
       fr           *trace.FlightRecorder
       snapshotOnce sync.Once
   )
   ```

   In `main`, before the `bg.Add(1 + numWorkers)` line (i.e. before workers
   launch):

   ```go
   fr = trace.NewFlightRecorder(trace.FlightRecorderConfig{
       MinAge:   5 * time.Second, // keep >= last 5s of trace...
       MaxBytes: 16 << 20,        // ...but never more than ~16 MiB
   })
   if err := fr.Start(); err != nil {
       log.Fatal(err)
   }
   defer fr.Stop()
   ```

   Say aloud, straight from the Go blog: *"MinAge is roughly 2× the window
   you'll want to analyze. Busy services produce 2–10 MB/s of trace; size
   MaxBytes to match. MaxBytes wins over MinAge — both are hints."*

4. **Live-code TODO 2.** In `worker`, replace the TODO 2 comment with a
   snapshot-once block:

   ```go
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

   Add `"os"` to the imports. Note the *condition* changed (added
   `&& fr.Enabled()`).

   Call out each detail explicitly — this is the whole exercise:
   - `sync.Once`: eight workers cross the threshold within microseconds
     during a convoy; you want **one** snapshot, not eight.
   - `go` before `snapshotOnce.Do`: `WriteTo` does real work. Don't add it
     to a live request's latency.
   - `fr.Enabled()`: cheap guard so the slow-path check stays quiet after
     you've already stopped the recorder.
   - The detect-then-`WriteTo` pattern is the point: the trigger fires
     *after* the interesting behavior; the ring buffer still contains it.

5. **Run it.**
   ```bash
   go run main.go
   ```
   Wait for `wrote flight recorder snapshot to flightrecorder.trace`.
   Point at the file size:
   ```bash
   ls -lh flightrecorder.trace
   ```
   A few hundred KB — not a 12s whole-run trace, and *definitely* not the
   GB you'd have on a real service.

6. **Open the snapshot.**
   ```bash
   go tool trace flightrecorder.trace
   ```

7. **View trace by proc.** Point at:
   - The first several seconds: dense "request confetti" — many short
     bursts of activity across the P rows as 8 workers churn through
     sub-ms requests.
   - Then: a wide, ~270ms stretch where **one** goroutine runs on one P and
     the other rows are dead quiet. Click it: `main.(*service).refresher`,
     inside the `refresh cache` region.
   - Point at the `refresh cache` region bar — a task/region we set up in
     the demo, doing its job here.

8. **Goroutine analysis.**
   - `main.(*service).worker` — Count 8, dominant column is
     **Block time (sync)**.
   - `main.(*service).refresher` — a single G with ~270ms of execution.
   - Ratio to say aloud: 8 × ~270ms of blocked-sync time — the workers
     spent ~2s of aggregate wall-clock parked on that one lock.

9. **Synchronization blocking profile.** From the landing page, or
   headless:
   ```bash
   go tool trace -pprof=sync flightrecorder.trace > sync.pprof
   go tool pprof -top sync.pprof
   ```
   Expect ~2s of cumulative delay concentrated on
   `sync.(*RWMutex).RLock` under `main.(*service).handle`. That's the
   convoy in pprof form.

10. **User-defined regions** (`/userregions`). `refresh cache` appears
    with a tiny handful of long durations amid many short ones. That's the
    "every fourth refresh" pattern from the source, quantified.

11. **State the diagnosis in one sentence.** *"Every fourth cache refresh
    does a full 250ms rebuild while holding the write lock; the eight
    workers convoy behind RLock."* Not "requests got slow" — a mechanism.

12. **(Bonus, if time.)** Show the fix from the README bonus section:
    build `next` outside the critical section, then a brief write lock for
    the pointer swap. Re-run: no SLOW lines, no snapshot fires. **Absence
    of the trigger is now evidence of the fix.**

## Fix
The exercise fix is the two TODOs above — the instrumentation, not a code
change to the service. For the bonus "actually fix the convoy":

```go
// Full revalidation: rebuild and verify OUTSIDE the lock...
next := make(map[int]string, cacheEntries)
for i := range cacheEntries {
    next[i] = fmt.Sprintf("value-%d-%d", n, i)
}
spin(250 * time.Millisecond)
// ...then a brief write lock for the pointer swap.
s.mu.Lock()
s.cache = next
s.mu.Unlock()
```

This requires moving locking out of `refresh` — the incremental branch still
wants the lock during its writes.

Verify:

```bash
go run main.go
```

Expected: no `SLOW request` lines, no `wrote flight recorder snapshot`
line. The flight recorder is still running; it just never trips. That's the
observability win — the same detector that revealed the incident now
watches over the fix.

Before/after: the broken snapshot's `Synchronization blocking profile`
shows ~2s in `RLock`; on the fixed program the profile is dominated by tiny
mutex contention orders of magnitude smaller.

## Ask the room
- Why is the snapshot pattern (`fr.WriteTo` on trigger) fundamentally more
  useful than `trace.Start` on a long-running service? What's the ratio of
  bytes shipped to bytes captured?
- Where in the trace do you *see* the convoy — which single view makes the
  eight-way pileup on `RLock` unmistakable?
- The `RWMutex` reads take the "read" lock; why do reads still queue behind
  a write lock at all? (Writer priority: pending writes block new readers.)
- What would you set `MinAge` and `MaxBytes` to for your own service, and
  what data would inform those numbers?

## Common pitfalls
- **The `sync.Once` and the extra goroutine are the whole lesson.** If
  students write the snapshot inline in `worker`, the eight-way convoy
  produces eight snapshot attempts *and* stalls each worker for the
  duration of `WriteTo`. Show them the wrong version if time — the trace
  itself gets weirder.
- **Forgetting `fr.Enabled()` guard.** After the first snapshot, `fr` is
  stopped. Without the guard, subsequent SLOW checks still call
  `snapshotOnce.Do` (a no-op) and log spam. Harmless, but ugly.
- **Not seeing SLOW lines at all.** Different hardware, different luck.
  Lower `slowThreshold` (say 50ms) or reduce `runFor` × increase odds by
  running twice. Keep a canned `flightrecorder.trace` as a fallback.
- **"Why not just `net/http/pprof`'s trace endpoint?"** Good question —
  answer: same trace format, but you'd need to know *when* to start. The
  flight recorder is what you use when you can't predict the moment.
- **Snapshot writes to CWD.** If students `go run` from odd directories,
  the file lands somewhere surprising. Verify with `ls -la` before opening.
