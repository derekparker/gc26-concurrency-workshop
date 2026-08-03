# Presenter: The Silent Stall

> Presenter-only. Students use README.md.

## Goal
Teach students to triage a hang the runtime *cannot* detect — a
fan-out/fan-in worker pool wedged on a self-deadlocking `sync.Mutex` —
using `goroutines -group userloc` and stack diffing across a worker
cohort.

## Reproduce
From the exercise directory:

```bash
cd 03-delve/exercises/ex1-fanout-fanin
go run .
```

Expected output (repeats forever, no crash, no fatal error):

```
[MONITOR] collected 37/200 results
[MONITOR] collected 37/200 results
[MONITOR] collected 37/200 results
...
```

Because the monitor goroutine loops on `time.Sleep(2s)`, at least one
goroutine is always runnable, so the runtime's "all goroutines are
asleep" detector never fires. That's the whole point of this exercise:
this is what a production hang looks like — up, health-checked,
completely wedged. `go vet` is clean; `go run -race .` reports nothing.

Getting a debugger onto it (show *both*, they're both worth practicing):

```bash
# A) run under Delve, let it stall, Ctrl+C at the prompt
dlv debug
(dlv) continue
# ...wait ~5s for the MONITOR lines to repeat, then Ctrl+C

# B) production style: build, run, attach by PID
go build -gcflags='all=-N -l' -o ingest .
./ingest &
dlv attach $(pgrep ingest)
```

## Root cause
`Stats.Record` locks `s.mu`, then calls `noteCritical` for
`SevCritical` events — and `noteCritical` **also** locks `s.mu`.
`sync.Mutex` is not reentrant: the goroutine is waiting for a lock it
already holds. Every other worker then piles up in front of that same
mutex. `noteCritical` is only reached on critical events; the first one
in the deterministic event stream is ID 36 (`i%37 == 36`), which is why
the stall always happens right around "collected 3x/200" (32–40 across
runs; don't quote a specific number on stage).

One cause → four symptoms visible in the goroutine survey:

- 8 workers wedged on `sync.Mutex.Lock`
- feeder wedged on `chan send` (workers stopped consuming)
- collector wedged on `chan receive` (workers stopped producing)
- the `wg.Wait` / `close(results)` goroutine wedged on `SemacquireWaitGroup`

## Walkthrough

### 1. Survey by location — one command, whole story

```
(dlv) goroutines -group userloc
```

```
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

**Say out loud:** every user goroutine is stuck. The story is written
in the wait reasons. The 8 workers on `sync.Mutex.Lock` are the mass;
everything else is a downstream symptom.

The transcript above is abridged to the group headers. On screen Delve
also prints up to 5 sample goroutine lines under each header — and
*those* lines are the only place the `[sync.Mutex.Lock]` / `[chan send]`
wait reasons actually appear. Point at them, not at the `Total:` counts.

Goroutine IDs will vary between runs — group and filter by *location*
or *label*, never by ID.

### 2. Isolate the mutex holder among 8 identical waiters

`sync.Mutex` doesn't record its owner. Find the holder the honest way:
print stacks for all 8 waiters and look for the odd one out.

```
(dlv) goroutines -with userloc mutex.go -t 8
```

Seven of them will look like this — one lock in flight, called from `Record`:

```
  Goroutine 6 - User: .../sync/mutex.go:46 sync.(*Mutex).Lock [sync.Mutex.Lock]
	...
	7  0x... in main.(*Stats).Record         at ./main.go:56
	8  0x... in main.worker                  at ./main.go:108
```

One will have **two** lock frames — the tell-tale sign:

```
  Goroutine 5 - User: .../sync/mutex.go:46 sync.(*Mutex).Lock [sync.Mutex.Lock]
	...
	6  0x... in sync.(*Mutex).Lock          at .../mutex.go:46
	7  0x... in main.(*Stats).noteCritical  at ./main.go:67
	8  0x... in main.(*Stats).Record        at ./main.go:61
```

Goroutine 5 is holding `s.mu` (from `Record:56`) and simultaneously
parked trying to acquire it again (from `noteCritical:67`). Self-deadlock.
The other 7 workers are queued behind it.

### 3. Confirm at the values

```
(dlv) goroutine 5
(dlv) frame 8
(dlv) print eventID
36
```

Event 36 is the first `FATAL` in the deterministic stream — the first
event that took the `SevCritical` branch through `noteCritical`. Bug had
been dormant since program start; the very first invocation of the
critical path is the last thing the program does.

## Fix

Don't lock inside `noteCritical`. Document that the caller must hold
`s.mu` — the standard Go pattern (often spelled with a `Locked` suffix):

```go
// noteCritical remembers critical event IDs.
// Caller must hold s.mu.
func (s *Stats) noteCritical(eventID int) {
	s.criticalIDs = append(s.criticalIDs, eventID)
}
```

Verify:

```bash
go run .
```

```
collected 200 results
severity counts: INFO=151 WARN=26 ERROR=18 FATAL=5
SUCCESS
```

Alternatives worth naming (don't dwell): restructure `Record` to do all
its work in one lock scope, or move `criticalIDs` to its own struct with
its own lock. **Non-fixes:** `sync.RWMutex` (also not reentrant); Go
deliberately has no reentrant mutex — if you're re-locking, your
invariants aren't clear.

## Ask the room

- The stall always fires at event 36 (the first FATAL). What made this
  deterministic, and how much harder would a probabilistic trigger be
  to debug?
- `pgrep`+attach vs `Ctrl+C` on a live Delve session — when do you have
  a choice, and when don't you?
- If this were a real service, what would you `dump` before fixing and
  restarting? Who reads the dump, and where?
- Why is `-race` silent here? Whose model of the bug does the race
  detector match, and whose does it miss?

## Common pitfalls

- **Reading by goroutine ID.** IDs are per-run. `goroutines -group
  userloc` and `-with userloc <substr>` are the durable moves.
- **Trusting `-race`.** No data race exists — all accesses are locked.
  Deadlocks and self-deadlocks are invisible to `-race`. Different tool,
  different bug class.
- **Reading code first.** Both `Record` and `noteCritical` look
  idiomatic in isolation. The bug lives in the *call chain*, which is
  exactly what a stack trace shows and diff review doesn't.
- **Attach on Linux.** May need `sysctl kernel.yama.ptrace_scope=0` or
  root. macOS handles the signing dance via Delve's own debugserver.

## Stretch: the goroutine leak profile

Only run this if the room is ahead of schedule — it's a great callback
in Part IV, and the README has full setup and captured output. The
one-line pitch:

```bash
# Go 1.26: experiment, gated at build time. On Go 1.27+ it's GA and the
# GOEXPERIMENT setting is deleted — drop the prefix.
GOEXPERIMENT=goroutineleakprofile go build -gcflags='all=-N -l' -o ingest .
INGEST_DEBUG_ADDR=127.0.0.1:8899 ./ingest &
# wait ~5s, then:
curl 'http://127.0.0.1:8899/debug/pprof/goroutineleak?debug=1'
```

The profile lists **only** goroutines the GC proves can't wake up
(11 here — the monitor and HTTP goroutines are correctly absent). The
one-goroutine bucket whose stack shows both `Record` and `noteCritical`
in flight is the culprit — the same odd-one-out you found by hand,
served fleet-scale, with no debugger attached.

Closing line: **profile says THAT (and where); Delve says WHY.** They
compose.
