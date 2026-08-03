# 01-race-detector: Finding Data Races with `-race`

## Overview

This section teaches Go's built-in data race detector: what a data race
actually is, how to read a race report, and how to choose the right fix
(mutex, RWMutex, atomic, or channel). You'll follow an instructor-led demo,
then debug three progressively sneakier buggy programs yourself, plus a
bonus lab on catching races in *tests* with `go test -race` and
`testing/synctest`.

A data race is two goroutines accessing the same memory location at the same
time, where at least one access is a write, with no synchronization ordering
the accesses. Races are the classic "works on my machine" bug: the program
may look correct for months and then corrupt data under production load. The
race detector turns that guesswork into a precise report with two stack
traces.

One more reason this matters in 2026: as AI agents write more of our code,
engineers spend less time writing and more time reviewing and debugging code
they didn't write. A race report is *ground truth* about what the program
actually did, paste one into your AI assistant and it fixes the real bug
instead of guessing. Mastering the tools that produce that evidence is the
skill that compounds.

## Schedule (~60 minutes)

| Time | Activity |
|------|----------|
| 0:00–0:20 | Instructor demo: [`demo/`](demo/), counter race, reading the report, three fixes |
| 0:20–0:30 | [Exercise 1: Stats Tracker](exercises/ex1-counter/), invisible counter races |
| 0:30–0:45 | [Exercise 2: Cache & Metrics](exercises/ex2-map/), flaky map crashes |
| 0:45–0:58 | [Exercise 3: Banking](exercises/ex3-banking/), "looks synchronized but isn't" |
| 0:58–1:00 | Wrap-up: when to run `-race`, what it can't find |
| Bonus | [Exercise 4: TTL Cache Tests](exercises/ex4-testing/), `go test -race` + `testing/synctest` (if time allows, or homework) |

## Using the Race Detector

Add `-race` to any build-style command:

```bash
go run -race .
go test -race ./...
go build -race .    # produces an instrumented binary for staging/canary
```

## How It Works (the 2-minute version)

The detector is built on ThreadSanitizer. Every memory access and every
synchronization operation (mutex lock/unlock, channel send/receive,
`WaitGroup.Wait`, `atomic` ops, goroutine start...) is instrumented. Using
vector clocks, the runtime tracks the **happens-before** relation defined by
the [Go memory model](https://go.dev/ref/mem): if two conflicting accesses
are not ordered by happens-before, that's a data race, *even if the accesses
didn't physically collide on this run*.

Two optimizations explain the price tag in [Cost and
Configuration](#cost-and-configuration):

- **Shadow memory.** A vector clock per goroutine is cheap; a vector clock
  per *memory word* would not be. Instead TSan keeps a small fixed number of
  **shadow cells** beside every word of your program's memory, each recording
  one recent access as a (goroutine, timestamp, read/write) triple, and
  decides from that sliding window rather than from unbounded history. This
  is wired straight into Go's allocator: every time the heap grows,
  `mallocgc` calls `racemapshadow` to tell TSan about the new range
  (`runtime/malloc.go`), and shadow exists only for heap and data/bss.
  Shadow scales with your program's memory footprint, which is *why* `-race`
  costs a multiple of your memory rather than a constant.
- **Epochs.** Comparing full vector clocks on every access would be
  O(goroutines) per access. The FastTrack insight (Flanagan & Freund, PLDI
  2009) is that the overwhelming majority of accesses are already cleanly
  ordered by some happens-before chain, so the common case collapses to a
  pair of scalar **epochs**, one (goroutine, timestamp) pair, making the
  check O(1). Only genuine concurrent read-sharing promotes back to a full
  vector clock. The vector clocks buy precision; not needing them most of
  the time buys the speed.

Two consequences worth internalizing:

- **No false positives.** If `-race` reports a race, it is a real race. Don't
  rationalize it away ("that's just a stats counter"), the compiler and CPU
  are allowed to miscompile racy code in surprising ways.
- **False negatives are possible.** The detector only sees code that actually
  *executes*, and only flags accesses that actually happen concurrently
  without ordering during that run. An untested code path, or a schedule where
  the two goroutines never overlap, produces no report. Coverage depends on
  exercising realistic concurrent workloads.

## Anatomy of a Race Report

This is the core skill of this hour. A report always has three parts:

```
WARNING: DATA RACE
Read at 0x00c00012a118 by goroutine 10:      ← (1) one access + its stack
  main.(*Bank).Transfer()
      .../ex3-banking/main.go:70 +0x124
  main.teller()
      .../ex3-banking/main.go:137 +0xc8

Previous write at 0x00c00012a118 by goroutine 8:   ← (2) the conflicting access
  main.(*Bank).Transfer()
      .../ex3-banking/main.go:84 +0x218
  ...

Goroutine 10 (running) created at:           ← (3) where each goroutine was started
  main.main()
      .../ex3-banking/main.go:188 +0x32c
```

How to read it:

1. **The two accesses.** The first stack is the access that *triggered* the
   report; "Previous write/read" is the earlier, conflicting access. At least
   one of the two is always a write. Same address (`0x00c...`), two
   goroutines, no ordering, that's the race.
2. **Read vs write matters.** Write/write races corrupt data; read/write
   races return torn or stale values. Multi-word values (`time.Time`, maps,
   slices, interfaces, strings) can be observed *half-written*.
3. **Goroutine creation sites** tell you which `go` statement launched each
   party, often the fastest way to identify *which* worker/monitor is
   involved when both stacks look identical.
4. One warning per racy *pair*, a single root cause (e.g. one unprotected
   struct) commonly produces many reports. Fix the shared state, not each
   report one at a time.

The address matches *within* a run, but don't expect it to reproduce across
runs, it's simply wherever the allocator happened to put the object.

Note the familiar `0x00c0...` in these reports, though. Go 1.26 randomizes
the heap base address at startup on 64-bit platforms as a hardening measure
(disable with `GOEXPERIMENT=norandomizedheapbase64`), but
race-instrumented builds **opt out**, ThreadSanitizer needs a fixed shadow
mapping, so `-race` keeps the classic arena addresses. Compare a trivial
`fmt.Printf("%p", new(int))`:

```
plain:  0x47172f584130   0x2ca2e170e020   0x5c0245294020
-race:  0xc000012188     0xc00009e038     0xc000096038
```

That's why every sample address in this section starts `0x00c...`.

## Cost and Configuration

- **Overhead:** typically 5–10× memory and 2–20× CPU. Fine for tests,
  CI, and canary deployments; usually too slow for every production replica.
- **Supported platforms:** linux (amd64, arm64, ppc64le, s390x, loong64, and
  riscv64 as of Go 1.26), darwin (amd64, arm64), windows/amd64,
  freebsd/amd64, netbsd/amd64. Requires cgo (on non-Darwin platforms a C
  compiler must be installed).
- **When a race is found:** the report goes to stderr and the program keeps
  running, then exits with status 66. Tune with `GORACE`:

```bash
GORACE="halt_on_error=1" go run -race .        # stop at the first race
GORACE="log_path=/tmp/race" go test -race ./...  # write reports to files
GORACE="strip_path_prefix=$PWD/" go run -race .  # shorter paths in reports
```

## Races in Tests and CI

- Run `go test -race ./...` in CI. Non-negotiable for concurrent code, it's
  the cheapest place to catch a race, and the exit code fails the build.
- The detector only finds races your tests *provoke*. Add tests that exercise
  concurrency deliberately (multiple goroutines hammering the API), and
  remember `-race` lowers throughput, set timeouts accordingly.
- **`testing/synctest`** (stable since Go 1.25) runs a test in a "bubble"
  with a fake clock, making concurrent tests fast and deterministic, and the
  race detector understands its synchronization, so `synctest` +`-race` is a
  powerful combination for testing concurrent code. The bonus lab,
  [Exercise 4](exercises/ex4-testing/), is a hands-on tour of both of these
  points: a green test suite hiding a race from CI, and a 2.4-second timing
  test rewritten to run in a bubble in ~0ms.
- **`sync.WaitGroup.Go`** (Go 1.25) replaces the error-prone
  `wg.Add(1)` / `go func() { defer wg.Done() ... }()` dance, you'll see it
  in the demo. You don't have to do the rewrite by hand: `go fix ./...`
  applies it for you via the `waitgroup` modernizer. (`go fix` was
  completely rebuilt in Go 1.26 as the home of Go's modernizers, on the same
  analysis framework as `go vet`. The analyzer is named `waitgroup` on the
  Go 1.26 you're running today, and is renamed `waitgroupgo` in 1.27.)

## What the Race Detector Won't Catch

- Races on code paths that never executed during the run.
- Races the schedule happened to order on this run (run tests repeatedly /
  under load; `-count=` helps).
- **Logical races** that aren't data races: check-then-act bugs where every
  individual access is synchronized but the *sequence* isn't atomic
  (exercise 3 discusses this).
- Deadlocks and goroutine leaks, that's what sections 02 and 03 are for.

## Looking Ahead: Go 1.27

Expected August 2026. Nothing changes in the race detector itself, everything
above still applies. What does move is the tooling around it:

- **`testing/synctest.Sleep`**, combines `time.Sleep` and `synctest.Wait`, so
  the usual "advance the fake clock, then let everyone settle" step is one call.
- **`httptest.NewTestServer`**, a `Server` on an in-memory fake network,
  designed for use inside a synctest bubble. This is what finally makes
  concurrent HTTP tests deterministic.
- **`go fix` gains `atomictypes`**, which rewrites primitive `sync/atomic`
  calls into the typed wrappers: `var x int32; atomic.AddInt32(&x, 1)` becomes
  `var x atomic.Int32; x.Add(1)`. Worth preferring regardless of the tool, the
  typed versions don't permit non-atomic access to the same variable (a common
  source of exactly the bugs this section is about) and they sidestep the
  64-bit alignment trap on 32-bit architectures.

## Further Reading

- [Data Race Detector manual](https://go.dev/doc/articles/race_detector)
- [The Go Memory Model](https://go.dev/ref/mem)
- [Testing concurrent code with testing/synctest](https://go.dev/blog/synctest)
- [ThreadSanitizer algorithm](https://github.com/google/sanitizers/wiki/ThreadSanitizerAlgorithm)
