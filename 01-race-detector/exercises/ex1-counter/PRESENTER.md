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
Total time: 208ms
```

Run it a handful of times. On Go 1.25+ the item total prints
5000 basically every time — which is the whole point of the exercise.

Occasionally (~1 run in 20) `Workers used` prints 4 instead of 5 — a lost
`s.workers++`. If it happens live, use it: the loss you *can* see is the
tip; `-race` finds the ones you can't.

Now flip on the detector:

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

5–8 warnings collapse to just those three racy fields. `startTime` is
written once in `NewStats` *before* any `go`, then only read — the goroutine
creation is a happens-before edge, so it's safe.

## Walkthrough

1. **"It works every time" reveal (2 min).** Run `go run .` two or three
   times. Total is always 5000. Ask: *"Would you ship this?"* Most people
   say yes. That's the trap.

2. **Turn on `-race` (3 min).** Run `go run -race .`. Point out:

   - Exit code 66 — race detector's tell. (That's the *program's* code;
     `go run` itself exits 1, so `echo $?` on stage shows 1. Build the
     binary if you want to show 66.)
   - "Found N data race(s)" at the bottom — usually 5–8, and it varies
     run to run. Do NOT try to fix N things.
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

4. **Inventory, don't chase (2 min).** Rather than click through every
   report, `grep` or eyeball for the distinct call sites in *your* code
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

The whole fix is in `solution.diff`, applied from this directory:

```bash
git apply solution.diff        # mutex + locked accessors, exactly as above
```

> Undo it with `git apply -R solution.diff` when you want the racy version
> back for the next session.

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
Total time: 208ms
```

No `WARNING: DATA RACE`, no `Found N data race(s)`, exit code 0.

## Ask the room

Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

**Q: Why did the total *always* print 5000 without `-race`, even though the
race is real? What is the detector tracking that a functional assertion
isn't?**

The detector doesn't check the *answer*, it checks the *synchronization*.
It maintains a vector clock per goroutine and shadow state per memory word;
it reports when two accesses to the same address — at least one a write —
have no happens-before edge between them. That's a property of the
program's ordering structure, independent of what values came out.

Why 5000 anyway: `s.processed += items` is load-add-store, a window of a
couple of nanoseconds. Workers publish once per 20ms, so the odds of two
landing inside that window are on the order of 1 in 10⁷ per pair per batch —
and there are only 50 publishes in the whole run. The race is present on
100% of runs; the *loss* is essentially never observed.

The line to land: **a passing assertion is evidence about one schedule. The
detector is evidence about the ordering, which holds across all of them.**

Bonus if someone asks why `-race` *does* show 4600: instrumentation costs
5–20× and perturbs the scheduler, which widens the collision window. The
visible corruption is a side effect of the tool, not the detection.

**Q: One of the racy fields is `lastUpdated`, a `time.Time` — three words
wide. Why is a torn `time.Time` scarier than a torn `int`? Could
`sync/atomic` fix it?**

A torn `int` gives you a wrong number, but still a *valid* `int`. A torn
`time.Time` gives you a value that never existed. It's three words —
`wall uint64`, `ext int64`, `loc *Location` — and since Go 1.9 it packs a
monotonic reading into `wall`/`ext`, where a flag bit in `wall` decides how
`ext` is interpreted. Mix halves across two writes and `time.Since` can
return negative, or centuries, because you read a monotonic value under
wall-clock rules. `IsStale` then flips at random.

The scarier part: `loc` is a *pointer*. A torn pointer word isn't a wrong
time, it's memory-unsafety — the one thing Go's type system otherwise
guarantees. (In practice the compiler emits word-sized loads and stores, so
you get a mix of intact words rather than a shredded pointer — but the
memory model promises you nothing, and this is exactly the reasoning that
stops working on a different architecture.)

Can atomics fix it? Not `atomic.Int64` — there's no counter to add. But yes
via `atomic.Pointer[time.Time]` (Go 1.19+) or `atomic.Value`: make the value
immutable and publish a pointer to a fresh one in a single atomic store.
That's the general move — atomics give you word-sized atomicity, so to make
a wide value atomic you make it immutable and swap the pointer.

It's still the wrong tool *here*, and this is the point worth making:
`processed` and `lastUpdated` are written together and read together. Two
separate atomics give you per-field atomicity and no consistency *across*
fields — the monitor can read a total from after the update and a timestamp
from before it. The mutex buys the invariant the atomics can't.

**Q: The monitor only *reads*. Concurrent reads of an `int` seem harmless —
why does the Go memory model still count that as a race?**

First, correct the premise: concurrent reads are *not* a race. `GetTotal`
races because it runs concurrently with `RecordWork`'s **write**. The
definition needs two accesses to the same location, from different
goroutines, at least one of them a write, unordered by happens-before.
Reader + writer qualifies; reader + reader never does.

Then the "but a stale int is harmless" objection, which is the real
question:

- You aren't promised a *stale* value — you're promised nothing. Without a
  happens-before edge the compiler may hoist the load out of the loop, keep
  it in a register, or fuse and split accesses. The classic is a racy
  `for !done {}` compiling to `if !done { for {} }`.
- The 2022 memory model revision states a racy read may observe any write
  to that location, or a zero — deliberately aligned with C++'s "no
  out-of-thin-air" wording. Not "stale but valid": undefined *in the spec*.
- The dangerous licence is the compiler's, not the CPU's. Marking a variable
  as raced tells the optimizer nothing; it optimizes as if single-threaded.

Worth saying out loud: an aligned 8-byte `int` genuinely will never tear at
the hardware level on amd64 *or* arm64. That's the trap — the hardware is
better behaved than the language, so the bug survives every test you run.
What differs on arm64 is *reordering*, not tearing: amd64 gives you strong
ordering almost for free, while arm64 will happily reorder loads and stores
around each other. Same racy source, and the demo laptop this repo is built
on is arm64.

**Q: Would `sync.RWMutex` help here? What would you measure before
switching?**

Almost certainly not, and it may well be slower. This workload is 5 writers
publishing every 20ms and *one* reader every 50ms — contention is
effectively zero and the critical sections are nanoseconds. `RWMutex` is a
bigger struct and does more atomic work on the read path: `RLock` atomically
bumps a shared `readerCount`, so concurrent readers ping-pong one cache
line between cores. It wins only with many concurrent readers, a low write
rate, and critical sections long enough (hundreds of ns and up) that real
reader parallelism outweighs that cache-line traffic.

What to measure before switching:

- A benchmark with `b.RunParallel` at your actual reader:writer ratio and
  `GOMAXPROCS` — not a microbenchmark of `Lock`/`Unlock` in a tight loop.
- Lock **hold** time vs **wait** time. `RWMutex` only pays off when hold
  time is long. If waits are ~0, there is nothing to win.
- A contention profile: `runtime.SetMutexProfileFraction(1)` and
  `go tool pprof` on the mutex profile, or the execution tracer's blocking
  view (that's section 02). If the mutex doesn't appear in the profile,
  don't touch it.
- `-race` again afterwards. Same happens-before guarantees, but swapping
  locks is exactly when someone writes a field under `RLock` — and the
  detector catches it.

If someone raises writer starvation: Go's `RWMutex` is write-preferring — a
pending `Lock` blocks new `RLock`s — so writers don't starve here, unlike
some other languages' implementations.

## Common pitfalls

- **Chasing warnings, not fields.** Students will try to "fix" seven
  reports. Redirect them to the three shared fields. One fix per field.
- **Adding `atomic.Int64` for the counters and calling it done.** That
  handles `processed`/`workers` but leaves `lastUpdated` — a multi-word
  `time.Time` — racy. Atomics can't help there; you need the mutex anyway.
- **Reentrant lock deadlock in `IsStale`.** Extremely common. It doesn't
  present as a hang — the runtime's deadlock detector kills it in about
  0.1s with `fatal error: all goroutines are asleep - deadlock!`. (You'd
  only get a true hang if some other goroutine were still ticking.) Go
  mutexes don't recurse; `sync.Mutex.Lock` on a mutex you already hold
  blocks forever.
- **Locking `NewStats`.** Not wrong, but unnecessary — no goroutine can see
  the `*Stats` before `NewStats` returns. Good time to mention that
  publication through a normal return value is a happens-before edge.
