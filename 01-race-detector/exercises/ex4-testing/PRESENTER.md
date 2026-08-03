# Presenter: Exercise 4 (Bonus) — The Test Suite That Lies

> Presenter-only. Students use README.md.

## Goal

Two lessons stacked into one bonus lab, both with concrete production
payoff:

1. **A green test suite means nothing for concurrent code unless `-race`
   was on.** The suite in this exercise has a `TestConcurrentAccess`, and
   it's been quietly *provoking* a data race on every run for months while
   CI reports PASS.
2. **Timing-based tests are choosing between slow and flaky.**
   `testing/synctest` (stable in Go 1.25) makes them instant, exact, and
   deterministic — and it stacks with `-race`.

## Reproduce

From `01-race-detector/exercises/ex4-testing`:

```bash
go test -v
```

```
=== RUN   TestSetGet
--- PASS: TestSetGet (0.00s)
=== RUN   TestConcurrentAccess
--- PASS: TestConcurrentAccess (0.00s)
=== RUN   TestExpiryAndSweep_RealClock
--- PASS: TestExpiryAndSweep_RealClock (2.40s)
=== RUN   TestExpiryAndSweep_Synctest
    ttlcache_test.go:119: TODO: rewrite TestExpiryAndSweep_RealClock using synctest.Test
--- SKIP: TestExpiryAndSweep_Synctest (0.00s)
PASS
ok  	ttlcache	2.809s
```

Green. Now the version CI *should* have been running:

```bash
go test -race
```

Expected (abridged):

```
==================
WARNING: DATA RACE
Read at 0x00c000156208 by goroutine 14:
  ttlcache.(*Cache).Get()
      .../ex4-testing/ttlcache.go:61 +0x194
  ttlcache.TestConcurrentAccess.func1()
      .../ex4-testing/ttlcache_test.go:50 +0x104

Previous write at 0x00c000156208 by goroutine 13:
  ttlcache.(*Cache).Get()
      .../ex4-testing/ttlcache.go:61 +0x1a8
  ttlcache.TestConcurrentAccess.func1()
      .../ex4-testing/ttlcache_test.go:50 +0x104

Goroutine 14 (running) created at:
  sync.(*WaitGroup).Go()
      /usr/local/go/src/sync/waitgroup.go:238 +0x6c
...
--- FAIL: TestConcurrentAccess (0.04s)
    testing.go:1712: race detected during execution of test
FAIL
exit status 1
```

## Root cause

### Part 1

`Get` takes `c.mu.RLock()` — correct for reading the `entries` map — and
then does `c.hits++` / `c.misses++` while holding it. `RWMutex.RLock`
admits any number of readers at once, so four test goroutines execute
`c.hits++` (a read-modify-write) simultaneously. Same bug as
exercise 3, on a stats counter this time. The code comment even lies about
it:

```go
c.misses++ // stats only, and we hold the lock, so this is safe
```

Note `swept` is fine: only written under exclusive `Lock` in `sweep` and
read under `RLock` in `Stats` — properly ordered.

### Part 2

`TestExpiryAndSweep_RealClock` sleeps `1200ms` when the true TTL boundary
is `1000ms`. That 200ms slack is load-bearing — tighten it and CI machines
under load start missing the boundary. So the test costs 2.4 real seconds
forever *and* can never assert anything sharper than "expired sometime in
the last 200ms." Multiply by every timing test in the monorepo and CI is
minutes slower than it should be.

## Walkthrough

### Part 1: What CI never ran (5 min)

1. **Show green (`go test -v`).** Point at `TestConcurrentAccess` — the
   test that literally exists to protect against this class of bug —
   passing.
2. **`go test -race`.** Read the report. Line 61 shows up as *both* the
   read and the previous write (`c.hits++`). That signature is
   exercise 1's `x++`.
3. **Zoom out.** What lock does `Get` hold at line 61? `RLock`. Exercise
   3 again: `RLock` doesn't exclude other `RLock` holders. The stats
   counter is racing itself across N concurrent readers.
4. **Ask: "Fix by taking `Lock()` in `Get`?"** Sure, silences the
   detector, kills read concurrency. `Get` is the hot path — do we want to
   serialize all reads on a stats counter?

   Better: `atomic.Int64`. Atomics under `RLock` are fine — same pattern
   as exercise 2's `HitCount`.

### Part 2: Deleting 2.4 seconds (10 min)

1. **Motivate synctest.** Read the `1200ms` line aloud from
   `TestExpiryAndSweep_RealClock`. Explain the slack trade-off (generous =
   slow forever, tight = flaky forever) and note the test can't assert
   sharp boundaries (`ttl-1ms` live, `ttl+1ms` dead) because real
   scheduling makes no such promise.

2. **The synctest mental model in three sentences.**

   - `synctest.Test(t, func(t *testing.T) { ... })` runs the closure in a
     **bubble**.
   - Inside the bubble, `time` uses a **fake clock** that only advances
     when every goroutine in the bubble is **durably blocked**.
   - `time.Sleep(1 * time.Second)` returns "immediately," after the clock
     jumps forward exactly 1s, firing any timers along the way (including
     the janitor's ticker).

3. **Write the test with the students.** Emphasize: `New` must be called
   **inside** the bubble — otherwise the janitor's ticker uses the real
   clock and everything unravels. Use `synctest.Wait()` after sleeping
   past the tick to guarantee the sweep has run before you assert
   `Len() == 0`.

## Fix

### Part 1 — atomic counters in `ttlcache.go`

```go
import (
	"sync"
	"sync/atomic"
	"time"
)

type Cache struct {
	mu      sync.RWMutex
	entries map[string]entry
	ttl     time.Duration

	hits   atomic.Int64
	misses atomic.Int64
	swept  int

	done chan struct{}
}

func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		c.misses.Add(1)
		return "", false
	}
	c.hits.Add(1)
	return e.value, true
}

func (c *Cache) Stats() (hits, misses, swept int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int(c.hits.Load()), int(c.misses.Load()), c.swept
}
```

### Part 2 — rewrite the timing test with `testing/synctest`

```go
import "testing/synctest"

func TestExpiryAndSweep_Synctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(1*time.Second, 2*time.Second)  // MUST be inside the bubble
		defer c.Close()

		c.Set("greeting", "hello")

		// 1ms before the TTL: still live. No slack — the fake clock
		// advances exactly as far as we tell it to.
		time.Sleep(999 * time.Millisecond)
		if _, ok := c.Get("greeting"); !ok {
			t.Fatal("entry should still be live 1ms before the TTL")
		}

		// 2ms later (t = ttl+1ms): expired, but not swept yet.
		time.Sleep(2 * time.Millisecond)
		if _, ok := c.Get("greeting"); ok {
			t.Fatal("entry should have expired 1ms after the TTL")
		}
		if got := c.Len(); got != 1 {
			t.Fatalf("Len() = %d, want 1 (expired but not swept yet)", got)
		}

		// Advance past the janitor's 2s tick, then wait for the bubble
		// to go quiet so the sweep has definitely finished.
		time.Sleep(1 * time.Second) // now at t ≈ 2.001s
		synctest.Wait()

		if got := c.Len(); got != 0 {
			t.Fatalf("Len() = %d, want 0 (janitor should have swept)", got)
		}
		if _, _, swept := c.Stats(); swept != 1 {
			t.Fatalf("Stats() swept = %d, want 1", swept)
		}
	})
}
```

The whole fix is in `solution.diff`, applied from this directory:

```bash
git apply solution.diff        # atomic counters + synctest rewrite, exactly as above
```

> Undo it with `git apply -R solution.diff` when you want the racy,
> real-clock version back for the next session.

Verify:

```bash
go test -race -v
go test -run Synctest -count=20   # 20 iterations, still under a second
```

Expected:

```
--- PASS: TestSetGet (0.00s)
--- PASS: TestConcurrentAccess (0.02s)
--- PASS: TestExpiryAndSweep_RealClock (2.40s)
--- PASS: TestExpiryAndSweep_Synctest (0.00s)
PASS
```

Once the students are convinced, delete `TestExpiryAndSweep_RealClock`.
The synctest version is *strictly better*: instant, deterministic, sharp
assertions, race-instrumented.

## Ask the room

Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- Plain `go test` passed for months with a real race in the hot path.
  What does an assertion-based test *fundamentally* miss that `-race`
  catches?

  An assertion checks the *result* of one particular schedule; `-race`
  checks the *ordering* itself, independent of any single run's outcome.
  `TestConcurrentAccess` only asserts `Len() == keys` and `hits != 0` — it
  never asserts an exact hit count, so even a run that loses several
  `hits++` increments to the race still satisfies both assertions. The race
  is present on 100% of runs (four goroutines take `RLock` and increment
  concurrently every single time), but the *value corruption* that would
  actually fail an assertion is a rare, load-dependent event — and this
  test's assertions are loose enough that it might never fail one even if
  it tried. `-race` doesn't care what value came out; it maintains a vector
  clock per goroutine and shadow state per memory word, and reports the
  moment two accesses to the same address, at least one a write, have no
  happens-before edge between them. That's a property of the program's
  synchronization structure, not of what numbers happened to land this run
  — which is exactly why it catches what months of green `go test` runs
  couldn't.

- The racy counters are "just stats" — a lost `hits++` fails no
  assertion. Why fix it anyway? (Recall the section intro: the compiler
  is *allowed* to miscompile racy code.)

  Because "racy but just stats" isn't a real category in the Go memory
  model — there's no tier of undefined behavior that's cheaper than
  another. The compiler and CPU generate code assuming *single-threaded*
  semantics for any access that isn't ordered by a happens-before edge; a
  racy `c.hits++` gives the compiler license to reorder it, keep the value
  in a register across iterations, hoist it out of a loop, or fuse/split
  the load-modify-store however it likes, because from its point of view
  nothing else could be touching that memory concurrently. Today, on this
  compiler and this architecture, that license is exercised mildly and you
  lose an occasional increment. Nothing guarantees it stays mild — the
  canonical failure mode of this exact class of bug is a racy condition
  variable (`for !done {}`) getting compiled to `if !done { for {} }`,
  which doesn't lose a count, it hangs forever. `swept` in this same struct
  is the contrast case worth pointing at: it's *also* just a stats field,
  but it's correctly ordered (`Lock` in `sweep`, `RLock` in `Stats`), so it
  costs nothing extra and buys you out of ever having this conversation
  about it.

- What does `time.Now()` return inside a synctest bubble? Why is that
  useful?

  A value on the bubble's fake clock, which starts at midnight UTC,
  2000-01-01, and only advances when every goroutine in the bubble is
  durably blocked — on `time.Sleep`, a bubble-associated channel or
  `select`, `sync.WaitGroup.Wait`, or `sync.Cond.Wait`. Nothing about wall
  time or the host machine's scheduler enters into it: `time.Sleep(1 *
  time.Second)` returns as soon as the bubble goes quiet, having jumped the
  clock forward exactly one second and fired any timers due along the way
  (here, the janitor's ticker). That's what makes the rewritten test both
  instant *and* exact — it can assert "still live at `ttl-1ms`, expired at
  `ttl+1ms`" because the fake clock advances precisely as far as told,
  where the real-clock version could only ever assert "expired sometime in
  the last 200ms of slack." Same behavior under test, zero wall-clock cost,
  sharper assertions.

- Why must the `Cache` be created *inside* `synctest.Test`? What happens
  if `New` runs outside and the returned `*Cache` is used inside?

  A goroutine (and any timer it starts) is only associated with the bubble
  if it's spawned, directly or transitively, from the function passed to
  `synctest.Test` — or if it operates on a channel or timer that already
  belongs to the bubble. `New` starts the janitor with `go
  c.janitor(sweepEvery)` and that goroutine calls `time.NewTicker`; if
  `New` runs *before* `synctest.Test` is entered, the janitor and its
  ticker are ordinary real-time citizens, invisible to the bubble's clock
  and to its "is everyone durably blocked" quiescence check. Then the test
  body's `time.Sleep` calls fast-forward the *bubble's* clock while the
  janitor keeps waiting on the *real* one — sleeping past `t=2s` in bubble
  time does nothing to a ticker that needs two actual wall-clock seconds to
  fire. This exercise's own common-pitfalls note nails the observed
  failure mode: the test hangs, or the `Len() == 0` assertion fails
  forever, because the sweep that's supposed to have happened by then
  simply hasn't. The fix is mechanical — call `New` from inside the
  closure passed to `synctest.Test`, as the "MUST be inside the bubble"
  comment on that line insists — but the *reason* is the association rule,
  worth saying explicitly: the bubble only governs what it created.

- Delete `defer c.Close()` in the synctest test and rerun. What do you
  see, and why is that a *feature* of synctest?

  An immediate, named failure instead of a silent pass:

  ```
  panic: deadlock: main bubble goroutine has exited but blocked goroutines remain
  ...
  goroutine 8 [select (durable), synctest bubble 1]:
  ttlcache.(*Cache).janitor(...)
  ```

  `synctest.Test` requires that when the bubble's main goroutine (the
  closure you passed in) returns, every other goroutine associated with
  the bubble has *also* exited — not just be durably blocked, actually
  gone. Forget `Close`, and the janitor is still parked in its `select`
  waiting on the ticker or `c.done`; that's exactly the state the bubble
  refuses to let you walk away from, so it panics and names the leaked
  goroutine and the line it's blocked on. That's a goroutine-leak detector
  you get for free, deterministically, on every single test run — not
  something that only shows up under `go tool pprof` on a production
  server three weeks after the `Close` call got refactored away. It's also
  the reason a type with a background goroutine *needs* a `Close`/`Stop`
  method in the first place: synctest just makes the cost of skipping it
  visible immediately instead of eventually.

## Common pitfalls

- **Creating `New(...)` outside the bubble.** The janitor's ticker
  captures the real clock; the fake clock in the bubble never fires it;
  test hangs or asserts `Len() != 0` forever. Point out the "inside the
  bubble!" comment on the `New` line.
- **Assuming `synctest.Wait()` is a `Sleep`.** It's a *rendezvous* — it
  blocks the calling goroutine until every *other* goroutine in the
  bubble is durably blocked. If nothing else is running, it returns
  instantly. Use it as a happens-before edge, not a delay.
- **Blocking on real I/O inside the bubble.** Real `net.Conn.Read` is
  *not* durable blocking (something outside the bubble could unblock it).
  Time won't advance, `Test` reports a deadlock. Use `net.Pipe` or
  in-memory fakes.
- **Using `t.Run` / `t.Parallel` inside the bubble.** Restricted.
  Mention it before someone learns the hard way.
- **Thinking synctest removes race non-determinism.** It doesn't.
  Goroutines still interleave between clock stops — which is exactly why
  `-race` still works. synctest kills *timing* nondeterminism, not
  *scheduling* nondeterminism.
- **Forgetting `defer c.Close()` — great teachable moment.** synctest
  requires every goroutine in the bubble to exit; forgetting `Close`
  leaves the janitor blocked in `select` and you get a nice
  goroutine-leak diagnostic:

  ```
  panic: deadlock: main bubble goroutine has exited but blocked goroutines remain
  ...
  goroutine 8 [select (durable), synctest bubble 1]:
  ttlcache.(*Cache).janitor(...)
  ```

  This is a free goroutine-leak detector — advertise it.
