# Exercise 2: Channel Forensics

**Time: ~15 minutes** | Difficulty: medium

## Problem

A build dispatcher: 3 workers, a `jobs` channel, a `reports` channel,
both buffered "generously". The unit tests (4 jobs) pass. A realistic
batch of 12 jobs dies instantly:

```bash
go run .
```

```
20:29:xx dispatched job 9 (pkg/service09)
fatal error: all goroutines are asleep - deadlock!
```

Same crash, every run — this deadlock is fully deterministic.

## Your Task

Diagnose the deadlock **using channel inspection**, not code reading. The
goal of this exercise is to come away fluent in what `print <channel>`
tells you. Then fix it so all 12 jobs build:

```
job 12 finished in 10.5ms
build complete
```

Rules of engagement: you may *only* look at the source **after** you can
answer these three questions from Delve alone:

1. How many jobs are sitting in the `jobs` buffer, and which ones?
2. Who is in the `sendq` of `reports`? Who is in its `recvq`?
3. Which line will `main` execute next, and which job is in its hand?

## Debugging

```bash
dlv debug
(dlv) continue        # Delve stops AT the fatal error, process frozen
```

## Hints

<details>
<summary>Hint 1: get to a frame where the channels are in scope</summary>

After the fatal-throw stop:

```
(dlv) goroutine 1
(dlv) bt
```

Find the first `main.main` frame in the backtrace (frame 3 here) and
select it:

```
(dlv) frame 3
(dlv) print job
```

`jobs`, `reports`, and the `job` currently in main's hand are all visible
from that frame.

</details>

<details>
<summary>Hint 2: interrogate the channels</summary>

```
(dlv) goroutines -chan jobs
(dlv) goroutines -chan reports
(dlv) print jobs
(dlv) print reports
```

For each channel note: `qcount` vs `dataqsiz` (how full), `sendq`
(goroutines parked sending), `recvq` (goroutines parked receiving), and
the waiter summary Delve prints underneath. A deadlock is always a cycle;
these four commands hand you every edge.

</details>

## Solution

<details>
<summary>Full walkthrough + fix</summary>

### Walkthrough (verified transcript)

Main is a sender, not a receiver:

```
(dlv) goroutine 1
(dlv) bt 4
3  0x... in main.main at ./main.go:79        <- jobs <- job
(dlv) frame 3
(dlv) print job
main.Job {ID: 10, Target: "pkg/service10"}
```

Answers to the three questions:

```
(dlv) print jobs
chan main.Job {
	qcount: 4,          <- buffer holds 4 jobs (6,7,8,9)
	dataqsiz: 4,        <- ...and 4 is its capacity: full
	...
	recvq: { first: nil, ... },        <- NO worker is asking for a job
	sendq: { first: 0x..., ... },}     <- main is parked here, holding job 10

Goroutines waiting on this channel:
* Goroutine 1 - User: ./main.go:79 main.main [chan send]
```

The buffer isn't opaque — those are real values you can read:

```
(dlv) print jobs.buf
*[4]main.Job [
	{ID: 8, Target: "pkg/service08",},
	{ID: 9, Target: "pkg/service09",},
	{ID: 6, Target: "pkg/service06",},
	{ID: 7, Target: "pkg/service07",},
]
```

(Ring-buffer order — `sendx`/`recvx` are the wrap indices.) Jobs 6–9 are
stuck in the buffer. In a real system this is how you learn *which* work
items are trapped in a wedged queue — which tenant, which request IDs.

```
(dlv) print reports
chan main.Report {
	qcount: 2,          <- 2 finished reports buffered
	dataqsiz: 2,        <- capacity 2: full
	recvq: { first: nil, ... },        <- NOBODY IS READING REPORTS
	sendq: { first: 0x..., ... },}     <- all three workers parked here

Goroutines waiting on this channel:
  Goroutine 7 - User: ./main.go:44 main.worker [chan send]
  Goroutine 8 - User: ./main.go:44 main.worker [chan send]
  Goroutine 6 - User: ./main.go:44 main.worker [chan send]
```

The picture, entirely from channel state:

- workers finished 2 jobs (buffered in `reports`) and hold 3 more,
  blocked sending their reports — because **`reports.recvq` is empty**
- nobody drains `reports` because main only starts reading reports in
  "phase 2", *after* dispatching all 12 jobs
- main can't finish dispatching because workers stopped taking jobs, so
  `jobs` (4) is full and main is wedged holding job 10
- 2 + 3 + 4 = 9 jobs accounted for; job 10 in main's hand; jobs 11–12
  never dispatched

The bug is the **phased design**: "dispatch everything, then collect
everything" only works if `len(batch) <= buffers + workers`. The unit
tests' 4 jobs fit; 12 don't. Buffer sizes chosen from a "typical burst"
are a time bomb.

### The fix

Dispatch and collect concurrently — either move dispatch into a goroutine:

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

...or keep dispatch in main and collect in a goroutine. Either breaks the
cycle. Growing the buffers to 12 also "fixes" this batch — ask yourself
what happens with batch 13. (Buffers change *when* senders block, never
*whether* a missing receiver deadlocks you.)

</details>

## Discussion Questions

- The runtime's dump also showed 4 goroutines in `chan send`. What did
  `print reports` tell you that the dump could not? (The dump has no
  buffer contents, no qcount, no "which channel is this", no recvq/sendq
  membership — you'd correlate four stacks by hand.)
- `qcount == dataqsiz` with a populated `sendq` is a signature. So is
  `qcount == 0` with a populated `recvq`. What failure mode does each
  smell like, respectively? (Consumer too slow/dead vs producer too
  slow/dead.)
- Where else have you shipped "buffer sized for the typical case"?
