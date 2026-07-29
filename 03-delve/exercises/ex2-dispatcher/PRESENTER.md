# Presenter: Channel Forensics

> Presenter-only. Students use README.md.

## Goal
Teach `print <chan>` as a first-class investigation tool. Students
should leave able to read `qcount`, `dataqsiz`, `sendq`, `recvq`, and
the buffer contents (`chan.buf`) off a wedged channel and reconstruct
the deadlock cycle from channel state alone — without reading `main.go`.

## Reproduce
From the exercise directory:

```bash
cd 03-delve/exercises/ex2-dispatcher
go run .
```

```
20:29:xx dispatching 12 jobs
20:29:xx dispatched job 1 (pkg/service01)
...
20:29:xx dispatched job 9 (pkg/service09)
fatal error: all goroutines are asleep - deadlock!
```

Deterministic: same crash, same jobs in the buffer, every run. Now do
it under Delve — the fatal throw *is* the breakpoint:

```bash
dlv debug
(dlv) continue
```

```
> [runtime-fatal-throw] runtime.fatal() /usr/local/go/src/runtime/panic.go:1241 (hits total:1)
```

## Root cause
The program is **phased**: `main` dispatches all 12 jobs, then reads
all 12 reports. With `numWorkers=3`, `cap(jobs)=4`, and `cap(reports)=2`,
this only works while `len(batch) <= cap(reports) + cap(jobs) +
numWorkers = 9`. The unit tests used 4 jobs. Twelve doesn't fit.

The accounting closes exactly:

- 2 reports buffered in `reports` (full, 2/2)
- 3 workers each parked in `chan send` holding a report, unable to push
  into `reports` — nobody is receiving
- 4 jobs sitting in the `jobs` buffer (full, 4/4)
- `main` parked in `chan send` holding job 10
- jobs 11 and 12 never dispatched

The bug is *structural*: "buffer sized for the typical burst" is a time
bomb. Buffers change *when* senders block, never *whether* a missing
receiver deadlocks you.

## Walkthrough

### 1. Get to a frame where the channels are named

At the fatal-error stop, `main` is at the top of `goroutine 1` but the
top frame is `runtime.gopark`. Climb to `main.main`:

```
(dlv) goroutine 1
(dlv) bt 4
```

```
0  0x... in runtime.gopark
1  0x... in runtime.chansend
2  0x... in runtime.chansend1
3  0x... in main.main                     at ./main.go:79
```

```
(dlv) frame 3
(dlv) print job
main.Job {ID: 10, Target: "pkg/service10"}
```

Main is stuck sending **job 10 of 12**. From this frame `jobs` and
`reports` are also in scope.

### 2. Interrogate `jobs`

Answer the students' three required questions from Delve alone:

```
(dlv) goroutines -chan jobs
```

```
* Goroutine 1 - User: ./main.go:79 main.main [chan send]
```

Only `main` is parked on `jobs`. Nobody is waiting to receive — no
worker is currently asking for a job.

```
(dlv) print jobs
```

```
chan main.Job {
	qcount: 4,          <- 4 jobs buffered
	dataqsiz: 4,        <- capacity 4: FULL
	...
	recvq: { first: nil, ... },        <- no receivers waiting
	sendq: { first: 0x..., ... },      <- main is parked here
}
```

The buffer isn't opaque:

```
(dlv) print jobs.buf
*[4]main.Job [
	{ID: 8, Target: "pkg/service08",},
	{ID: 9, Target: "pkg/service09",},
	{ID: 6, Target: "pkg/service06",},
	{ID: 7, Target: "pkg/service07",},
]
```

Ring-buffer order — `sendx`/`recvx` are the wrap indices. Jobs 6–9 are
trapped. **Say this out loud:** in a real system this is how you learn
which specific work items are stuck in a wedged queue — which tenant,
which request ID.

### 3. Interrogate `reports`

```
(dlv) print reports
```

```
chan main.Report {
	qcount: 2,          <- 2 finished reports buffered
	dataqsiz: 2,        <- capacity 2: FULL
	recvq: { first: nil, ... },        <- NOBODY IS READING REPORTS
	sendq: { first: 0x..., ... },      <- all three workers parked here
}

Goroutines waiting on this channel:
  Goroutine 7 - User: ./main.go:44 main.worker [chan send]
  Goroutine 8 - User: ./main.go:44 main.worker [chan send]
  Goroutine 6 - User: ./main.go:44 main.worker [chan send]
```

**This is the smoking gun.** `qcount == dataqsiz` with a populated
`sendq` and empty `recvq` is a signature: consumer too slow or dead. In
this program the consumer isn't slow — it doesn't exist yet, because
main is still in "phase 1: dispatch".

### 4. Draw the cycle from channel state alone

Everything above, without reading a line of `main.go`:

- workers finished 2 jobs (buffered in `reports`) and hold 3 more,
  blocked sending — `reports.recvq` is empty
- nobody drains `reports` because main only reads reports in "phase 2",
  after dispatching all 12 jobs
- main can't finish dispatching because workers stopped taking jobs —
  `jobs` (4/4) is full and main is wedged holding job 10
- 2 (buffered) + 3 (in-worker) + 4 (buffered) = 9 jobs accounted for;
  job 10 in main's hand; 11–12 never dispatched

## Fix

Break the phase barrier: dispatch and collect *concurrently*.

Option A — dispatch in a goroutine, collect in main:

```go
go func() {
	for _, job := range batch {
		jobs <- job
	}
	close(jobs)
}()

for range batch {
	r := <-reports
	log.Printf("job %d finished in %s", r.JobID, r.Duration)
}
```

Option B — keep dispatch in main, put collection in a goroutine. Either
breaks the cycle.

**Non-fix worth naming:** growing the buffers to 12 "fixes" *this*
batch. What happens with batch 13? Buffers move where senders block,
not whether the program deadlocks.

Verify:

```bash
go run .
```

```
job 12 finished in 10.5ms
build complete
```

## Ask the room

- The runtime's dump also showed 4 goroutines in `chan send`. What did
  `print reports` tell you that the dump could not? (Buffer contents,
  `qcount`/`dataqsiz`, which channel, `sendq`/`recvq` membership — the
  dump makes you correlate four stacks by hand.)
- `qcount == dataqsiz` with a populated `sendq` is a signature. So is
  `qcount == 0` with a populated `recvq`. What failure mode does each
  smell like? (Consumer too slow/dead vs producer too slow/dead.)
- Where in your own systems have you shipped "buffer sized for the
  typical case"?
- The unit tests (4 jobs) passed. What would you change in the test
  suite to catch this structurally, not by luck?

## Common pitfalls

- **Evaluating `jobs` from the wrong frame.** At the fatal-throw stop
  the current frame is `runtime.gopark`; `jobs` isn't in scope there.
  Always `frame 3` (the `main.main` frame) first.
- **Reading `qcount`/`dataqsiz` off the top of `print <chan>` and
  stopping.** The `sendq`/`recvq` summary Delve prints *underneath* the
  struct is where the parked goroutine list lives — scroll down.
- **Skipping `.buf`.** The buffer contents turn the demo from "two
  numbers" to "here are the actual work items". Don't skip.
- **Buffers as a fix.** Everyone in the room will suggest it. Have the
  batch-13 counterexample ready.
