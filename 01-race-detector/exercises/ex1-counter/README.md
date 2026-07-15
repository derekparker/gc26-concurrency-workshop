# Exercise 1: The Stats Tracker That Looks Fine (~10 min)

## The Situation

A worker pool reports progress through a shared `Stats` tracker: five workers
record completed batches, and a monitor goroutine prints throughput. It's
been running in production for months. Nobody has ever seen a wrong number.

Run it yourself:

```bash
go run .
```

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

Run it ten times. It prints 5000 every time. So it's correct... right?

## Your Task

1. Run the program under the race detector:

   ```bash
   go run -race .
   ```

2. For **each distinct warning**, identify from the report alone:
   - which field is being fought over,
   - which two goroutines are involved (use the "created at" stacks),
   - whether it's read/write or write/write.
3. Fix every race. `go run -race .` must finish with no warnings and still
   print `Total items processed: 5000`.

## Reading Your First Report

You should see several warnings (typically 7–8). They collapse to just
**three racy fields** — remember: fix the shared state, not each warning.
For example:

```
WARNING: DATA RACE
Read at 0x00c000098048 by goroutine 8:
  main.(*Stats).RegisterWorker()
      .../ex1-counter/main.go:34 +0x84
Previous write at 0x00c000098048 by goroutine 12:
  main.(*Stats).RegisterWorker()
      .../ex1-counter/main.go:34 +0x98
```

Same line appearing as both the read and the write is the signature of an
unsynchronized `x++`.

## Questions to Discuss

- Why did the total *always* print 5000 even though the race is real?
- One of the racy fields is a `time.Time`. Why is a torn `time.Time` worse
  than a torn `int`? Could you fix it with `sync/atomic`?
- The monitor only *reads*. Why does a concurrent read still count as a race?

<details>
<summary><strong>Hint 1</strong> (which fields?)</summary>

The reports point at three fields of `Stats`: `processed`, `workers`, and
`lastUpdated`. Writers: the five `processItems` goroutines via `RecordWork`
and `RegisterWorker`. Readers: the same workers *plus* `monitorProgress` via
`GetTotal` / `GetWorkerCount` / `GetTimeSinceUpdate`.

</details>

<details>
<summary><strong>Hint 2</strong> (why does it look correct?)</summary>

Workers publish only once per 20 ms batch, so two read-modify-write
sequences almost never physically overlap — the *loss* is rare, but the
*race* (no happens-before between accesses) is constant, and `-race` flags
the race, not the loss. In production, more workers + more load = the 4990s
start appearing. This is the whole sales pitch for the tool.

</details>

<details>
<summary><strong>Solution</strong></summary>

Add a mutex to `Stats` and hold it in every method that touches the fields:

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

func (s *Stats) GetTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processed
}
```

...and the same for `RegisterWorker`, `GetWorkerCount`, `GetLastUpdated`,
and `GetTimeSinceUpdate` (`startTime` is written once before the goroutines
start, then only read — publishing it via `NewStats` before any `go`
statement is a happens-before edge, so `GetElapsedTime` needs no lock; guard
it anyway if that subtlety makes you nervous).

Watch out for a classic deadlock: if `IsStale` locks and then calls
`GetTimeSinceUpdate` which locks again, you self-deadlock (Go mutexes are
not reentrant). Either have `IsStale` call the locking getter and take no
lock itself, or split out an unexported unlocked helper.

Alternatives worth discussing:

- `processed` and `workers` could be `atomic.Int64`, but `lastUpdated` is a
  multi-word `time.Time` — atomics can't help; you'd need the mutex anyway.
  Once you have a mutex, simplest is to use it for everything.
- `sync.RWMutex` fits the "one writer group, frequent readers" shape, but at
  this scale a plain `Mutex` measures the same. Don't reach for `RWMutex`
  without a benchmark.

</details>
