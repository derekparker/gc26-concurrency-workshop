# Exercise 1: The Silent Stall

**Time: ~20 minutes** | Difficulty: warm-up

## Problem

A fan-out/fan-in log-ingest pipeline: 200 events fanned out to 8 workers,
results fanned back in to a collector. It's a small program and it looks
reasonable, every shared structure is mutex-protected, `go vet` is clean,
and **`go run -race .` reports nothing**.

Run it:

```bash
go run .
```

```
[MONITOR] collected 37/200 results
[MONITOR] collected 37/200 results
[MONITOR] collected 37/200 results
...forever...
```

(The exact number varies 32–40 run to run; the stall itself does not.)

No crash. No `fatal error: all goroutines are asleep`, the monitor
goroutine keeps a timer alive, so the runtime's deadlock detector never
fires. This is what production hangs actually look like: the process is
"up", the health check pings, and nothing moves.

## Your Task

1. Use Delve to find out **where** every goroutine is stuck.
2. Identify the **one goroutine** whose position explains all the others.
3. Fix the bug (it's a one-line class of fix) and get:

```
collected 200 results
severity counts: INFO=151 WARN=26 ERROR=18 FATAL=5
SUCCESS
```

## Getting a Debugger onto a Hung Process

Two equally good moves, practice both:

```bash
# A: run it under Delve, let it stall, then interrupt
dlv debug
(dlv) continue
# ...wait for the MONITOR lines to repeat, then press Ctrl+C

# B: production style, it's already running, attach to it
go build -gcflags='all=-N -l' -o ingest .
./ingest &
dlv attach $(pgrep ingest)
```

## Hints

<details>
<summary>Hint 1: the survey command</summary>

```
(dlv) goroutines -group userloc
```

Group first, read stacks later. You have ~18 goroutines; the grouping
collapses them into a handful of lines with counts. Which group holds
your 8 workers? What wait reason do they show?

</details>

<details>
<summary>Hint 2: they're all waiting on the same thing</summary>

All 8 workers show `[sync.Mutex.Lock]`. A mutex that never comes back is
held by *someone*. A Go `sync.Mutex` doesn't record its owner, so find the
holder the honest way: look at the stacks of the waiters, 7 of them will
look identical, and one will be different. Print all of them in one shot:

```
(dlv) goroutines -with userloc mutex.go -t 8
```

(`-t 8` prints stack frames 0–8 under each matching goroutine — nine lines,
not eight. Frame 8 is exactly where the tell lives, which is why 8.)

</details>

<details>
<summary>Hint 3: the odd one out</summary>

Seven workers are parked at the `s.mu.Lock()` at the top of
`Stats.Record`. One worker's stack has **two** `Lock` calls in flight:

```
6  sync.(*Mutex).Lock          mutex.go:46
7  main.(*Stats).noteCritical  main.go:67
8  main.(*Stats).Record        main.go:61
```

`Record` locked the mutex... then called `noteCritical`, which locks the
same mutex. `sync.Mutex` is not reentrant: that goroutine is waiting for
itself. Everyone else is waiting for it. Why did nothing happen until the
first FATAL event (event 36)? Because `noteCritical` is only reached for
critical events, the code path was never exercised until then.

</details>

## Solution

<details>
<summary>Full walkthrough + fix</summary>

### Walkthrough (verified transcript, goroutine IDs will vary)

```
(dlv) goroutines -group userloc
.../main.go:167 in main.main.func3
	Total: 1                             <- feeder: blocked sending an event
.../main.go:185 in main.main
	Total: 1                             <- collector: blocked receiving a result
/usr/local/go/src/runtime/proc.go:463 in runtime.gopark
	Total: 6                             <- runtime housekeeping, ignore
/usr/local/go/src/runtime/sema.go:114 in sync.runtime_SemacquireWaitGroup
	Total: 1                             <- the wg.Wait/close(results) goroutine
/usr/local/go/src/runtime/time.go:363 in time.Sleep
	Total: 1                             <- the monitor (the reason there was no fatal error)
/usr/local/go/src/sync/mutex.go:46 in sync.(*Mutex).Lock
	Total: 8                             <- ALL EIGHT WORKERS
```

Story so far: workers wedged on a mutex → no results → collector starves;
workers never return → `wg.Wait` never returns → `results` never closes.
One cause, four symptoms.

Now find the mutex holder among the 8:

```
(dlv) goroutines -with userloc mutex.go -t 8
  Goroutine 5 - User: .../sync/mutex.go:46 sync.(*Mutex).Lock [sync.Mutex.Lock]
	...
	7  0x... in main.(*Stats).noteCritical   at ./main.go:67
	8  0x... in main.(*Stats).Record         at ./main.go:61
  Goroutine 6 - User: .../sync/mutex.go:46 sync.(*Mutex).Lock [sync.Mutex.Lock]
	...
	7  0x... in main.(*Stats).Record         at ./main.go:56
	8  0x... in main.worker                  at ./main.go:108
  ...six more identical to goroutine 6...
```

Goroutine 5 is the culprit and the victim: `Record` (holding `s.mu`)
called `noteCritical`, which blocked acquiring `s.mu`. Self-deadlock;
the other 7 workers then piled up behind it.

### The fix

Don't lock in `noteCritical`; document that it requires the caller to
hold the lock (this is the standard Go pattern, often spelled with a
`...Locked` suffix):

```go
// noteCritical remembers critical event IDs.
// Caller must hold s.mu.
func (s *Stats) noteCritical(eventID int) {
	s.criticalIDs = append(s.criticalIDs, eventID)
}
```

Alternatives worth discussing: restructure `Record` to do all its work
under one lock scope, or make the critical-ID list its own structure with
its own lock. **Non-fix:** `sync.RWMutex` doesn't help (also not
reentrant), and Go deliberately has no reentrant mutex, if you're
re-locking, your invariants are already unclear (see Russ Cox's classic
"experience report" on recursive mutexes).

### Why the other tools miss this

- `-race`: no data race exists. All accesses are (over-)synchronized.
- Runtime deadlock detector: only fires when *every* goroutine is asleep;
  the monitor's `time.Sleep` keeps the process technically alive.
- Reading the code: `Record` and `noteCritical` are both individually
  idiomatic, the bug only exists in the call chain, which is exactly
  what a stack trace shows and a diff review doesn't.

</details>

## Stretch: The Goroutine Leak Profile

Go ships a goroutine *leak* profile, a pprof profile that reports only
goroutines the runtime has proven can never wake up. Detection rides on
the garbage collector: if a goroutine is blocked on a channel/mutex/etc.
that is unreachable from any runnable goroutine (or anything a runnable
goroutine could unblock), it's leaked. A goroutine in `time.Sleep` (our
monitor) or `IO wait` will wake up, so it never appears, which is
exactly the noise the plain `goroutine` profile makes you filter by hand.

The profile is named `goroutineleak`
(`runtime/pprof.Lookup("goroutineleak")`, or
`/debug/pprof/goroutineleak` once you import `net/http/pprof`). In
**Go 1.26** it's an experiment gated at build time; in **Go 1.27** it's
generally available and the `GOEXPERIMENT` setting is deleted. This
program has a production-style opt-in debug endpoint for exactly this,
set `INGEST_DEBUG_ADDR`:

```bash
# Go 1.26 — the GOEXPERIMENT prefix is required:
GOEXPERIMENT=goroutineleakprofile go build -gcflags='all=-N -l' -o ingest .

# Go 1.27 and later — drop the prefix:
go build -gcflags='all=-N -l' -o ingest .

INGEST_DEBUG_ADDR=127.0.0.1:8899 ./ingest &
# wait for the MONITOR lines to start repeating (~5s), then:
curl 'http://127.0.0.1:8899/debug/pprof/goroutineleak?debug=1'
```

Captured output (addresses vary; runtime frames elided — real stacks also
carry `internal/sync.(*Mutex).lockSlow` and `sync.(*Mutex).Lock` between
`runtime_SemacquireMutex` and the `main.*` frame):

```
goroutineleak profile: total 11
7 @ 0x100eb475c 0x100e946d4 0x100e946b5 0x100eb5c68 0x100ec2ff0 ...
#	0x100eb5c67	internal/sync.runtime_SemacquireMutex+0x27	.../runtime/sema.go:95
#	0x1010516db	internal/sync.(*Mutex).Lock+0x7b		.../internal/sync/mutex.go:70
#	0x101051680	main.(*Stats).Record+0x20			./main.go:56
#	0x101051a3f	main.worker+0x6f				./main.go:108

1 @ 0x100eb475c 0x100e946d4 0x100e946b5 0x100eb5c68 0x100ec2ff0 ...
#	0x100eb5c67	internal/sync.runtime_SemacquireMutex+0x27	.../runtime/sema.go:95
#	0x101051820	main.(*Stats).noteCritical+0x20			./main.go:67
#	0x101051727	main.(*Stats).Record+0xc7			./main.go:61
#	0x101051a3f	main.worker+0x6f				./main.go:108

1 @ ...	main.main.func3+0x4f	./main.go:167       <- the feeder, [chan send]

1 @ ...	main.main+0x2bb		./main.go:185       <- main, [chan receive]

1 @ ...	sync.(*WaitGroup).Wait+0xa7 ... main.main.func2+0x23  ./main.go:160
```

Eleven leaked goroutines, zero false positives, the monitor and the
HTTP goroutines are correctly absent. And look closely: the odd one out
from Hint 3 is *right there*, a one-goroutine bucket whose stack has
`Record` **and** `noteCritical` in flight. (`?debug=2` prints full
stacks instead, with wait reasons annotated:
`goroutine 6 [sync.Mutex.Lock (leaked)]`.)

**That + where, but not why.** The profile is proof you *have* a leak
and shows where every leaked goroutine is parked, perfect for a CI
check or a fleet-wide sweep, and it works on processes you'd never
attach a debugger to. What it can't do is tell you *why*: no variable
values, no "which channel is this and who else is on it", no
`print <chan>`, no watchpoints. In this exercise the stacks happen to be
damning; in ex2's dispatcher the same profile would show four goroutines
in `chan send` and leave you to guess which channel and what's in the
buffer. The workflow that scales: **leak profile says THAT (and where);
Delve says WHY.**

Caveats worth saying out loud:

- **On Go 1.26, it's a build-time flag.** Without the `GOEXPERIMENT` build
  the endpoint 404s (`Unknown profile`). The runtime work was already done;
  the experiment was only about the API shape. Go 1.27 makes the profile
  generally available and deletes the `goroutineleakprofile` GOEXPERIMENT
  setting, so on 1.27+ the flag is not just unnecessary, it's gone.
- Detection is *conservative*: it can miss leaks whose primitive stays
  reachable from a global or from a runnable goroutine's locals. Absence
  of proof isn't proof of absence.
- Requesting the profile **triggers a full GC cycle** that performs the
  leak analysis (that's how it stays zero-overhead when unused). Fine on
  demand or in CI; don't scrape it every second.
- `?debug=2` prints *all* goroutines (leaked ones annotated `(leaked)`),
  like the classic `goroutine?debug=2` dump; `?debug=1` and the default
  proto output contain only the leaked ones.

## Discussion Questions

- The stall always happens at event 36 (the first FATAL). What property of
  this exercise made it *deterministic*, and how much harder would a
  probabilistic trigger be to debug?
- `pgrep`+attach vs `Ctrl+C`: when do you get a choice, and when don't you?
- If this were a real service, what would you capture (`dump`?) before
  fixing and restarting it?
