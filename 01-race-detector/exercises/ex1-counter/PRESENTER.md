# Presenter: Exercise 1 — The Stats Tracker That Looks Fine

> Presenter-only. Students use README.md.

## Goal

Show students that a program can print the "correct" number every run and
still be broken. Use `-race` to expose three unsynchronized fields on a
shared `Stats` struct and drive home the rule: **fix shared state, not
individual warnings.**

## Reproduce

From `01-race-detector/exercises/ex1-counter`:

```bash
go run .
```

Expected (looks perfect):

```
Starting 5 workers...
[Monitor] active - 1500 items by 5 workers (28918 items/sec)
[Monitor] active - 2500 items by 5 workers (24667 items/sec)
Worker 2 halfway: 2800 items so far (last update 0s ago)
...
Processing complete!
Workers used: 5 (expected: 5)
Total items processed: 5000 (expected: 5000)
```

Run it a handful of times. On this Go 1.25+ setup it prints 5000 basically
every time — which is the whole point of the exercise. Now flip on the
detector:

```bash
go run -race .
```

Expected (abridged):

```
==================
WARNING: DATA RACE
Read at 0x00c000114048 by goroutine 10:
  main.(*Stats).RegisterWorker()
      .../ex1-counter/main.go:34 +0x84
Previous write at 0x00c000114048 by goroutine 8:
  main.(*Stats).RegisterWorker()
      .../ex1-counter/main.go:34 +0x98
...
Total items processed: 4600 (expected: 5000)
Found 7 data race(s)
exit status 66
```

Note that with `-race` on, throughput drops enough that the loss becomes
visible in `Total items processed`. Without `-race`, workers publish once
per 20ms — the physical collision window is tiny, so the *loss* is rare
even though the *race* is constant.

## Root cause

`Stats` has zero synchronization. Three fields are written from five worker
goroutines and read from those same goroutines *plus* the monitor:

- `processed` — `RecordWork` does `s.processed += items` (read-modify-write).
- `workers` — `RegisterWorker` does `s.workers++`.
- `lastUpdated` — a multi-word `time.Time`, written in both methods.

7–8 warnings collapse to just those three racy fields. `startTime` is
written once in `NewStats` *before* any `go`, then only read — the goroutine
creation is a happens-before edge, so it's safe.

## Walkthrough

1. **"It works every time" reveal (2 min).** Run `go run .` two or three
   times. Total is always 5000. Ask: *"Would you ship this?"* Most people
   say yes. That's the trap.

2. **Turn on `-race` (3 min).** Run `go run -race .`. Point out:

   - Exit code 66 — race detector's tell.
   - "Found 7 data race(s)" at the bottom — do NOT try to fix 7 things.
   - Same address `0x00c...` appearing in multiple reports = same field.

3. **Read the first report line-by-line (3 min).** Same file, same line
   number in both stacks (`main.go:34`), one is a read, one is a write. That
   signature — one line as both read *and* write — is unsynchronized
   read-modify-write (`s.workers++`). Show them:

   ```go
   // main.go:34
   s.workers++
   ```

   Show the "created at" stacks: both goroutines were launched from
   `main.go:141` (`go processItems(...)`) — the workers racing each other,
   as expected.

4. **Inventory, don't chase (2 min).** Rather than click through all seven
   reports, `grep` or eyeball for the distinct call sites in *your* code
   (ignore `waitgroup.go`, `runtime/*`). Students should land on three
   fields: `workers`, `processed`, `lastUpdated`. Reports on line 28
   (`s.processed += items`) and lines 29/35 (`s.lastUpdated = time.Now()`)
   fill out the set.

5. **The monitor is a *reader* — why does that race? (1 min).**
   `monitorProgress` calls `GetTotal`, `GetWorkerCount`, `IsStale`. Reads
   with no lock while writers are running = race. The Go memory model
   requires happens-before between concurrent accesses when at least one is
   a write; unsynchronized reads count.

## Fix

Add a `sync.Mutex` to `Stats` and hold it in every method that touches the
racy fields:

```go
type Stats struct {
	mu          sync.Mutex
	processed   int
	workers     int
	startTime   time.Time
	lastUpdated time.Time
}

func (s *Stats) RecordWork(items int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processed += items
	s.lastUpdated = time.Now()
}

func (s *Stats) RegisterWorker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers++
	s.lastUpdated = time.Now()
}

func (s *Stats) GetTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processed
}
// ...same for GetWorkerCount, GetLastUpdated, GetTimeSinceUpdate
```

`GetElapsedTime` is safe unlocked — `startTime` is only written once before
any goroutine starts. Lock it too if you want uniformity; costs nothing.

**Watch for the deadlock trap when you demo `IsStale`:** the original code
has `IsStale` call `GetTimeSinceUpdate`, which now locks. If you also lock
`IsStale`, you self-deadlock (Go mutexes are not reentrant). Two clean
options:

- Have `IsStale` call the locking getter and take no lock itself.
- Split out an unexported unlocked helper (`timeSinceUpdateLocked`).

Verify:

```bash
go run -race .
```

Clean output:

```
Starting 5 workers...
[Monitor] active - 1500 items by 5 workers (28450 items/sec)
...
Processing complete!
Workers used: 5 (expected: 5)
Total items processed: 5000 (expected: 5000)
```

No `WARNING: DATA RACE`, no `Found N data race(s)`, exit code 0.

## Ask the room

- Why did the total *always* print 5000 without `-race`, even though the
  race is real? What is the detector tracking that a functional assertion
  isn't?
- One of the racy fields is `lastUpdated`, a `time.Time` — two words wide.
  Why is a torn `time.Time` scarier than a torn `int`? Could `sync/atomic`
  fix it?
- The monitor only *reads*. Concurrent reads of an `int` seem harmless —
  why does the Go memory model still count that as a race?
- Would `sync.RWMutex` help here? What would you measure before switching?

## Common pitfalls

- **Chasing warnings, not fields.** Students will try to "fix" seven
  reports. Redirect them to the three shared fields. One fix per field.
- **Adding `atomic.Int64` for the counters and calling it done.** That
  handles `processed`/`workers` but leaves `lastUpdated` — a multi-word
  `time.Time` — racy. Atomics can't help there; you need the mutex anyway.
- **Reentrant lock deadlock in `IsStale`.** Extremely common. If a student
  reports "my program hangs after the fix," it's this. Go mutexes don't
  recurse; `sync.Mutex.Lock` on a mutex you already hold blocks forever.
- **Locking `NewStats`.** Not wrong, but unnecessary — no goroutine can see
  the `*Stats` before `NewStats` returns. Good time to mention that
  publication through a normal return value is a happens-before edge.
