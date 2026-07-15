# Exercise 4 (Bonus): The Test Suite That Lies (~15–20 min)

> **Bonus / capstone lab.** Do this if you finish exercises 1–3 early, or
> take it home. Unlike the others, the bug here isn't in a program you run —
> it's in a library's *test suite*, which is where you'll actually meet most
> races in real life.

## The Situation

Your team maintains `ttlcache`, a small in-memory cache: entries expire
after a TTL, and a background janitor goroutine sweeps expired entries every
so often. It has tests — including a concurrency test! — and CI has been
green for months:

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

Run it ten more times — it passes every single time. Still, two complaints
came out of the last retro:

1. Nobody ever checked whether the tests would actually *catch* a
   concurrency bug.
2. The suite takes almost **3 seconds** for a 120-line package that does no
   I/O. Multiply that by every timing-dependent test in the monorepo and CI
   is minutes slower than it should be.

Both complaints are the same lab.

## Part 1: CI Never Ran `-race`

The CI job runs `go test ./...` — no `-race`. Run what CI *should* have been
running:

```bash
go test -race
```

```
==================
WARNING: DATA RACE
Read at 0x00c0001be208 by goroutine 12:
  ttlcache.(*Cache).Get()
      .../ex4-testing/ttlcache.go:61 +0x194
  ttlcache.TestConcurrentAccess.func1()
      .../ex4-testing/ttlcache_test.go:50 +0x104
  sync.(*WaitGroup).Go.func1()
      /usr/local/go/src/sync/waitgroup.go:258 +0x54

Previous write at 0x00c0001be208 by goroutine 14:
  ttlcache.(*Cache).Get()
      .../ex4-testing/ttlcache.go:61 +0x1a8
  ...
==================
--- FAIL: TestConcurrentAccess (0.16s)
    testing.go:1712: race detected during execution of test
FAIL
exit status 1
FAIL	ttlcache	2.799s
```

The exact test that was supposed to protect you — `TestConcurrentAccess` —
has been *provoking* a data race on every run for months, and plain
`go test` grinned and said PASS. The detector needs the flag; the flag costs
one line of CI YAML.

**Your task:**

1. Read the report. Line 61 shows up as both the read *and* the write —
   you know that signature from exercise 1. Now look at what lock `Get`
   holds at that line. You know *that* disease from exercise 3.
2. Fix the race so `go test -race` passes on every run. Constraint: `Get`
   is the hot path — try to keep concurrent readers concurrent.

## Part 2: The Test That Costs 2.4 Seconds Forever

Look at `TestExpiryAndSweep_RealClock`. It verifies real behavior worth
testing — entries expire at the TTL, the janitor sweeps them on its next
tick — by *actually waiting* for wall-clock time to pass:

```go
time.Sleep(1200 * time.Millisecond) // ttl is 1s... plus 200ms of slack
```

That slack is load-bearing: shrink the sleeps toward the true 1s/2s
boundaries and the test starts flaking on loaded CI machines, because the
real scheduler makes no timing promises. So every timing test in your
codebase faces the same lousy trade: **generous slack (slow forever) or
tight slack (flaky forever)**. Note it can't even assert anything *sharp* —
"still alive at 999ms, expired at 1001ms" is untestable with a real clock.

`testing/synctest` (stable since Go 1.25) ends the trade-off. Inside
`synctest.Test(t, func(t *testing.T) {...})` your function runs in a
**bubble**: the `time` package uses a fake clock, and time only advances
when every goroutine in the bubble is durably blocked. `time.Sleep(1 *
time.Second)` returns "immediately" — after the clock jumps forward exactly
1s and any timer that fires on the way (like the janitor's ticker) runs.

**Your task:**

1. Fill in `TestExpiryAndSweep_Synctest` (currently a `t.Skip` stub): the
   same three behaviors as the real-clock test — live before the TTL,
   expired-but-unswept after it, gone after the janitor's tick — inside a
   `synctest.Test` bubble. No slack: assert at `ttl-1ms` and `ttl+1ms`.
2. It must pass with `go test -run Synctest -count=20` in well under a
   second total.
3. Run the whole suite with `go test -race` — race-detector instrumentation
   works inside bubbles, so a fixed Part 1 plus your new test should give
   you a fast, deterministic, race-checked suite.
4. Delete `TestExpiryAndSweep_RealClock` (or keep it briefly to compare):

```
--- PASS: TestExpiryAndSweep_RealClock (2.40s)
--- PASS: TestExpiryAndSweep_Synctest (0.00s)
```

## Questions to Discuss

- Plain `go test` passed for months with a real race in the hot path. What
  two things does an assertion-based test miss that `-race` sees?
- The racy counters are "just stats" — a lost `hits++` fails no assertion.
  Why fix it anyway? (Recall the section README: the compiler is allowed to
  miscompile racy code.)
- What does `time.Now()` return inside a bubble? (Try printing it.) Why must
  the `Cache` be created *inside* `synctest.Test` for the fake clock to
  govern its janitor?
- Delete the `defer c.Close()` in your synctest test and run it. What
  happens, and why is that a *feature*?

<details>
<summary><strong>Hint 1</strong> (Part 1 — the race)</summary>

`Get` takes `c.mu.RLock()` — correct for reading the map — but then does
`c.misses++` / `c.hits++` while holding it. An `RWMutex` admits any number
of readers simultaneously, so four test goroutines execute `c.hits++`
(a read-modify-write) at the same time. The comment in the code — "we hold
the lock, so this is safe" — is exercise 3's lesson again: `RLock` orders
you against *writers*, not against other readers who cheat.

Fixes that keep readers concurrent: make `hits`/`misses` `atomic.Int64`
(atomics under an `RLock` are fine and idiomatic — same pattern as
exercise 2's `HitCount`), or move stats out of `Get`. Taking the full
`Lock()` in `Get` also silences the report, but serializes the hot path.

Note `swept` needs nothing: it's only written under the exclusive `Lock` in
`sweep` and read under `RLock` in `Stats`, which the memory model orders.

</details>

<details>
<summary><strong>Hint 2</strong> (Part 2 — synctest starter)</summary>

Shape of the test:

```go
import "testing/synctest"

func TestExpiryAndSweep_Synctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(1*time.Second, 2*time.Second) // inside the bubble!
		defer c.Close()
		// ... Set, Sleep, assert ...
	})
}
```

Facts you need:

- The bubble's clock starts at midnight UTC 2000-01-01 and advances only
  when every goroutine in the bubble is durably blocked (`time.Sleep`,
  channel ops on bubble channels, `select` over bubble channels...).
- The janitor is in the bubble because `New` is called in the bubble, so
  its ticker uses the fake clock too. Sleeping past t=2s *is* the tick
  firing.
- `synctest.Wait()` blocks until every *other* goroutine in the bubble is
  durably blocked — call it after sleeping past the tick to guarantee the
  sweep has finished before you assert `Len() == 0`.

</details>

<details>
<summary><strong>Solution</strong></summary>

**Part 1** — atomic counters; everything else in `ttlcache.go` unchanged:

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

**Part 2** — the synctest rewrite:

```go
func TestExpiryAndSweep_Synctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(1*time.Second, 2*time.Second)
		defer c.Close()

		c.Set("greeting", "hello")

		// 1ms before the TTL: still live. No slack needed — the fake
		// clock advances exactly as far as we say.
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
		time.Sleep(1 * time.Second) // now at t = 2.001s
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

Verification (real numbers from this machine):

```
$ go test -race -v
--- PASS: TestSetGet (0.00s)
--- PASS: TestConcurrentAccess (0.02s)
--- PASS: TestExpiryAndSweep_RealClock (2.40s)
--- PASS: TestExpiryAndSweep_Synctest (0.00s)
PASS
```

Note the synctest test passing *under `-race`*: the detector understands
the happens-before edges synctest's scheduler creates, so bubbles and
`-race` compose. A fake clock that hid races from the detector would be
worthless — this one doesn't.

**Forget `Close`?** If you drop `defer c.Close()`, the test fails
immediately:

```
panic: deadlock: main bubble goroutine has exited but blocked goroutines remain
...
goroutine 8 [select (durable), synctest bubble 1]:
ttlcache.(*Cache).janitor(...)
```

`synctest.Test` requires every goroutine in the bubble to exit — it names
the leaked goroutine and where it's blocked. You get a goroutine-leak
detector for free, which is why libraries with background goroutines need a
`Close`/`Stop` in the first place.

**What synctest can and cannot do:**

- The fake clock only governs the bubble. Anything created *outside*
  (a cache from `TestMain`, a global ticker) keeps real time, and operating
  on a bubbled channel/timer from outside the bubble panics.
- Only some operations are **durably** blocking: `time.Sleep`, operations on
  channels created in the bubble, `select` over bubble channels,
  `sync.WaitGroup.Wait` (bubble-associated), `sync.Cond.Wait`. Blocking on
  **real I/O — network reads, file reads, syscalls — or on `sync.Mutex` is
  not durable**, because something outside the bubble could unblock it. A
  goroutine blocked in a real `net.Conn.Read` means time never advances and
  `Test` eventually reports a deadlock — use in-memory fakes (e.g.
  `net.Pipe`) for network code.
- Inside the bubble, the `*testing.T` is restricted: no `t.Run`,
  no `t.Parallel`, no `t.Deadline`.
- synctest removes *timing* nondeterminism, not *interleaving*
  nondeterminism — goroutines still race each other between clock stops.
  That's a feature: `-race` still has real interleavings to inspect.

**Takeaways:**

1. `go test` green means nothing for concurrent code unless `-race` was on.
   It's one flag in CI — after this hour, adding it is the highest-value
   change you can ship.
2. Tests that sleep real time are choosing between slow and flaky.
   `testing/synctest` makes time-based tests instant, exact, and
   deterministic — and it stacks with `-race`.

</details>
