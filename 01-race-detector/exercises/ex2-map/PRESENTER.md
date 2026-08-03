# Presenter: Exercise 2 — The Flaky Service

> Presenter-only. Students use README.md.

## Goal

Two takeaways in one exercise:

1. Go's runtime "concurrent map writes" fatal is a **tripwire, not a
   detector** — it tells you almost nothing. `-race` tells you everything.
2. **One-size-fits-all locking is wrong.** Two maps with different
   read/write ratios want different synchronization. And a struct behind a
   map pointer needs its *own* story.

## Reproduce

From `01-race-detector/exercises/ex2-map`:

```bash
go run .   # repeat 5–10 times
```

Most runs finish cleanly:

```
=== Final Statistics ===
Cache entries: 62
Overall cache hit rate: 93.2% (929 hits, 68 misses, 9 invalidations)
Total operations recorded: 634
```

Some runs crash with the runtime map guard:

```
fatal error: concurrent map writes

goroutine 10 [running]:
internal/runtime/maps.fatal(...)
main.(*Service).StoreInCache(...)
      .../ex2-map/main.go:52
main.cacheWorker(0x2, ...)
      .../ex2-map/main.go:127 +0x2c0
```

Now the detector — every run:

```bash
go run -race .
```

Expected (abridged; you will get *many* pairs — 60–80+ on my machine):

```
WARNING: DATA RACE
Read at 0x00c00012c120 by goroutine 9:
  runtime.mapassign_fast64ptr()
  main.(*Service).RecordMetric()
      .../ex2-map/main.go:76
Previous write at 0x00c00012c120 by goroutine 10:
  main.(*Service).RecordMetric()
      .../ex2-map/main.go:76
...
Found 82 data race(s)
exit status 66
```

## Root cause

Four distinct problems hide in the report:

1. **`cache` map** is written in `StoreInCache` / `InvalidateCacheEntry`,
   read in `GetFromCache`, iterated in `GetCacheStats` — no
   synchronization.
2. **`metrics` map** is `m[k]++`ed from six goroutines in `RecordMetric`;
   read in `GetMetric`/`GetAllMetrics` — no synchronization.
3. **`CachedResult.HitCount++`** in `GetFromCache` writes to the *struct
   behind* the map's pointer. Locking the map doesn't protect what its
   values point to.
4. **`GetCacheStats`** reads `HitCount` on every entry while workers
   increment them — the same struct field race, different reader.

## Walkthrough

1. **Show the flake (2 min).** `for i in $(seq 5); do go run .; done`.
   Most runs pass, one or two crash with `fatal error: concurrent map
   writes`. Read the crash aloud:

   ```
   fatal error: concurrent map writes
   ...
   main.(*Service).StoreInCache(.../ex2-map/main.go:52)
   ```

   Ask: *"Who was the other writer?"* The crash names **one** goroutine.
   You have no idea who lost the coin toss on the other side. This is the
   Go runtime's built-in map tripwire — cheap, best-effort, only fires when
   two accesses **physically** collide on the map. It's not the race
   detector.

2. **Flip on `-race` (3 min).** `go run -race .`. Now every run reports
   many warnings — 60+ is normal — and they come as **pairs of stacks**
   with both goroutines. Point out that `-race` catches the read/write pair
   even when the map runtime *wouldn't have* — races on maps, races on
   plain fields, all in one pass.

   Note: `-race` doesn't disable the runtime's map guard. Roughly 1 run in
   6 still dies on `fatal error: concurrent map writes` partway through,
   before the `Found N data race(s)` summary. If that happens on stage,
   use it — the tripwire fired first and the detector never got to finish
   its report — then just re-run.

3. **Group the reports (5 min).** Instead of reading all 80, sort by
   *function*:

   - Reports pointing at `RecordMetric` / `GetMetric` / `GetAllMetrics` →
     the metrics map.
   - Reports pointing at `StoreInCache` / `GetFromCache` /
     `InvalidateCacheEntry` / `GetCacheStats` (with `mapassign` /
     `mapaccess` in the stack) → the cache map.
   - Reports pointing at `GetFromCache` **without** a map frame — an
     address the detector saw a write to at line 41 (`result.HitCount++`)
     and a read to at line 69 (`totalHits += result.HitCount`). This is
     the sneaky one — it lives *behind* the map.

   Draw the students' attention to that third category: even if you slap a
   perfect lock on the map, this race survives.

4. **The two maps want different tools (3 min).**

   - `metrics` map: workload is ~90% writes. `RWMutex` gives you nothing —
     everyone needs the write lock. `sync.Map` documents itself as
     optimized for two specific patterns (write-once-read-many keys, and
     disjoint key sets per goroutine) and says it "should not be used for
     most code" — neither pattern is a write-heavy counter. Plain
     `sync.Mutex`.
   - `cache` map: read-mostly, keys stable. Many concurrent readers, rare
     writers. `sync.RWMutex` is the shape.

5. **The trap that survives every lock choice — live (5 min).** Don't just
   narrate this, run it. Four diffs, applied in order from
   `01-race-detector/exercises/ex2-map`, each building on the last:

   | Step | Command | Result |
   |------|---------|--------|
   | 0 | (none) | `main.go` as committed: the racy version |
   | 1 | `git apply locks.diff` | `RWMutex` on cache, `Mutex` on metrics — `HitCount` still a plain `int` |
   | 2 | `git apply atomic.diff` | `HitCount` → `atomic.Int64` — **breaks the build on purpose** |
   | 3 | `git apply atomic-fix.diff` | fixes `verifier`, the exercise's official fix |

   Reset with `git checkout -- main.go` (all diffs applied cumulatively;
   `git apply -R <name>` undoes one step if you want to back up instead of
   resetting everything).

   **Apply `locks.diff` and run `go run -race .`.** Every map operation is
   now guarded — `cacheMu.RLock`/`Lock` around the cache, `metricsMu.Lock`
   around metrics. Ask the room: *"Is this correct now?"* Most will say
   yes. Run it:

   ```
   Found 3 data race(s)
   ```

   All three point at `GetFromCache` (`main.go:47`, `result.HitCount++`)
   and `GetCacheStats` (`main.go:81`, `totalHits += result.HitCount`) — the
   exact category 3 report from step 3, unchanged. **The map is perfectly
   locked and the program still races.** `HitCount++` is a write that lives
   on the cache's *read* path (`GetFromCache` runs under `RLock`), and
   `GetCacheStats` reads the same field under its own `RLock`. `RWMutex`
   lets N readers run concurrently — and now N readers concurrently mutate
   `HitCount`. Locking the map bought nothing for what the map's *values*
   point to.

   Three options; the atomic wins:

   - Make `HitCount` an `atomic.Int64` — fine under `RLock`, no blocking.
   - Take the full `Lock` in `GetFromCache` — kills read concurrency; the
     whole point of `RWMutex` gone.
   - Drop per-entry hit counts, use the metrics map for hits.

   **Apply `atomic.diff` and rebuild.** It **fails to compile**, on purpose:

   ```
   ./main.go:279:5: invalid operation: hitCount2 <= hitCount1
        (operator <= not defined on struct)
   ```

   `atomic.diff` only touches the `CachedResult` struct and the cache's own
   methods (`GetFromCache`, `StoreInCache`, `GetCacheStats`) — it
   deliberately leaves `verifier`'s two direct `HitCount` reads alone. Stop
   here and say it out loud, **it's the point of the exercise**: changing
   one field's type handed the compiler an exhaustive list of every place
   that field is touched. Making a field atomic is a change to its
   *contract*, and the type system enforces the new contract everywhere.
   Compare that to the racy version, where the same field was read from
   three goroutines and nothing complained at all.

   **Apply `atomic-fix.diff`.** It's two lines — `verifier` now calls
   `.Load()` at both call sites (`main.go:269` and `:277`). Run
   `go run -race .` a handful of times — clean every time, exit 0, and the
   hit rate is unchanged (~93%). This is the exercise's official fix:
   `RWMutex` on the cache map, `Mutex` on the metrics map, `atomic.Int64`
   for the field living behind the cache's pointers.

6. **Bonus: measure before reaching for `sync.Map` (5 min, time permitting).**
   The cache's access pattern — stable keys, read-mostly, and disjoint per
   worker (`user_%d_%d`, each `cacheWorker` only touches its own keys) — is
   *exactly* what `sync.Map`'s docs describe as its sweet spot. Ask the
   room: *"So should we have used `sync.Map` from the start?"* Let them
   argue it both ways, then measure instead of guessing:

   ```bash
   git apply syncmap.diff   # on top of the official fix — cache becomes sync.Map
   go run -race .           # still clean
   go test -bench=. -benchmem -run=^$ -benchtime=2s -count=3 .
   ```

   Then back up one step and run the same benchmark against the `RWMutex`
   version for comparison:

   ```bash
   git apply -R syncmap.diff
   go test -bench=. -benchmem -run=^$ -benchtime=2s -count=3 .
   ```

   `bench_test.go` drives `GetFromCache`/`StoreInCache` concurrently at the
   same ~95/5 read/write ratio the real workers use, with each goroutine
   confined to its own key range — the same shape as the real workload, not
   a synthetic worst case. On the machine this was written on (Apple M1, 8
   cores):

   ```
   RWMutex + atomic:  ~136-152 ns/op   2 B/op   0 allocs/op
   sync.Map:          ~160-164 ns/op   5 B/op   0 allocs/op
   ```

   **`sync.Map` is ~15% *slower* here** — on a workload that matches its
   documented use case almost exactly. Land the actual lesson: the docs
   describing your access pattern is a reason to *try* a tool, not a reason
   to *trust* it. `sync.Map`'s `any`-typed `Load`/`Store` and its internal
   read/dirty-map bookkeeping cost more than `RWMutex`'s cache-line traffic
   recovers, at least at this contention level and core count. Numbers will
   differ on other hardware — that's exactly why you run the benchmark
   yourself instead of repeating this table.

   End on `git apply -R syncmap.diff` (or `git checkout -- main.go` then
   reapply `locks.diff` + `atomic.diff` + `atomic-fix.diff`) so the room
   leaves with the `RWMutex` version as the answer, not `sync.Map`.

## Fix

The official fix is `locks.diff` + `atomic.diff` + `atomic-fix.diff` applied
in sequence (see walkthrough step 5) — `RWMutex` guarding the cache map,
`Mutex` guarding the metrics map, `atomic.Int64` for `HitCount`. Full end
state:

```go
type Service struct {
	cacheMu sync.RWMutex
	cache   map[string]*CachedResult

	metricsMu sync.Mutex
	metrics   map[string]int
}

type CachedResult struct {
	Value      string
	ComputedAt time.Time
	HitCount   atomic.Int64
}

func (s *Service) GetFromCache(key string) (*CachedResult, bool) {
	s.cacheMu.RLock()
	result, exists := s.cache[key]
	s.cacheMu.RUnlock()

	if exists {
		result.HitCount.Add(1)
		s.RecordMetric("cache.hits")
		return result, true
	}
	s.RecordMetric("cache.misses")
	return nil, false
}

func (s *Service) StoreInCache(key, value string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache[key] = &CachedResult{Value: value, ComputedAt: time.Now()}
}

func (s *Service) InvalidateCacheEntry(key string) {
	s.cacheMu.Lock()
	delete(s.cache, key)
	s.cacheMu.Unlock()
	s.RecordMetric("cache.invalidations")
}

func (s *Service) GetCacheStats() (entries int, totalHits int) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	for _, result := range s.cache {
		entries++
		totalHits += int(result.HitCount.Load())
	}
	return
}

func (s *Service) RecordMetric(metric string) {
	s.metricsMu.Lock()
	s.metrics[metric]++
	s.metricsMu.Unlock()
}

func (s *Service) GetMetric(metric string) int {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	return s.metrics[metric]
}

func (s *Service) GetAllMetrics() map[string]int {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	result := make(map[string]int, len(s.metrics))
	maps.Copy(result, s.metrics)
	return result
}
```

Verify — should be clean every run, and the flakiness is gone:

```bash
go run -race .
```

```
Starting cache workers...
...
=== Final Statistics ===
Cache entries: ~60
Overall cache hit rate: ~93% (~900 hits, ~65 misses, ~5 invalidations)
Total operations recorded: ~630
```

No warnings, exit 0. Loop it 20× if you want to make the flakiness point
concrete.

`syncmap.diff` (applies on top of `locks.diff` + `atomic.diff` +
`atomic-fix.diff`) swaps the cache to `sync.Map` for the bonus benchmark in
walkthrough step 6 — not part of the official fix, see that step for why.

## Ask the room

Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- Why does the plain run only crash *sometimes*, while `-race` complains
  *every* time? What does the detector track that the map tripwire doesn't?

  The runtime map guard isn't a race detector — it's a flag check. Every map
  write sets a `hashWriting`-style bit on the map header for the duration of
  the operation and clears it after; if a write observes the bit already
  set, or a read observes it set, that's the `fatal error: concurrent map
  writes` (or `concurrent map read and map write`) panic. That only fires
  when two accesses **physically overlap in time** on the same map header —
  and a map write is a handful of instructions. Two of the six-plus
  goroutines hitting `RecordMetric` have to land their tiny critical windows
  on top of each other for the flag to catch it. Most of the time they
  don't; the writes interleave cleanly at the instruction level and the
  program finishes looking correct. It's also *only* a write/write (and
  read/write) check on map operations specifically — it has no idea
  `CachedResult.HitCount++` even exists, because that's not a map access at
  all.

  `-race` doesn't wait for a physical collision. It's shadow-memory and
  vector-clock tracking (ThreadSanitizer-style): every load and store is
  instrumented, and the runtime keeps a per-goroutine vector clock plus
  recent-access history per memory word. It reports a race whenever it sees
  two accesses to the same address, from different goroutines, at least one
  a write, with no happens-before edge between them — regardless of whether
  they landed at the same nanosecond. That's a property of the program's
  *synchronization structure*, not of one run's timing luck, which is why it
  shows up on every run: the missing lock is there every time, even when the
  scheduler doesn't happen to make it visible.

- `GetAllMetrics` uses `maps.Copy` to snapshot the map. Copying is a *read*
  operation — why is it still racy against writers?

  "Copying is a read" is true at the level of *your* intent, not at the
  level of what the runtime executes. `maps.Copy(dst, src)` (main.go:87 in
  the racy version, `main.go:324` in the fix) walks `src` bucket by bucket
  and issues a `mapaccess`-style read for every entry, then a `mapassign`
  into `dst`. Meanwhile `RecordMetric` (main.go:76) is doing `mapassign` on
  that same `src` map from other goroutines — and a map write in Go isn't
  just "set one value," it can grow the map, split buckets, and move
  entries between old and new bucket arrays. If `maps.Copy`'s iterator is
  mid-walk over a bucket that a concurrent write is relocating, it can read
  a tophash byte and a key/value slot that no longer agree with each other,
  follow a bucket pointer that's being swapped out from under it, or (in the
  worst case) see memory that isn't in a consistent map-shaped state at all.
  The result *value* `maps.Copy` hands you back is a new, uncontended map —
  but getting there required many individual reads of memory that a writer
  was concurrently mutating, and each of those reads is what `-race` (and,
  on a bad day, the runtime's own guard) is flagging. "Read-only from the
  caller's perspective" and "no shared-memory access during the call" are
  different claims, and only the second one is race-free.

- The race on `CachedResult.HitCount` survives even if you lock every map
  operation perfectly. Where does that race live, and why is a lock on the
  *map* the wrong tool for it?

  It lives one hop past the map, on the heap, in memory the map doesn't own
  at all. `s.cache[key]` gives you back a `*CachedResult` — a pointer that
  the map stored, not the struct itself. Once `GetFromCache` (main.go:39-44
  in the racy version) has that pointer in hand, `result.HitCount++` never
  touches the map's buckets again; it's a plain read-modify-write on an
  `int` field at a fixed heap address, indistinguishable to the runtime from
  any other unsynchronized shared-field race. A `cacheMu` mutex — even a
  perfectly-placed one — only ever serializes the *map operation* that hands
  out the pointer. It has zero relationship to what any goroutine does with
  that pointer afterward. Worse, under `RWMutex` specifically: `RLock` lets
  multiple readers run `GetFromCache` concurrently by design, and every one
  of them can fetch the *same* pointer for the same hot key and race to
  increment the *same* `HitCount` — the walkthrough's step 5 confirms this
  exactly, `locks.diff` leaves 3 races, all on `main.go:47`
  (`result.HitCount++`) and the `GetCacheStats` read. The fix isn't a bigger
  lock scope, it's recognizing the field needs its *own* synchronization
  story independent of the map's — here, `atomic.Int64`, which is correct
  under a shared `RLock` because the increment itself is a single atomic
  instruction with no read-modify-write window for two goroutines to land
  in.

- `sync.Map`'s docs describe exactly the cache's access pattern (stable,
  disjoint-per-goroutine keys, read-mostly) — yet the benchmark showed it
  *losing* to `RWMutex` here. What does that tell you about trusting a
  "documented fit" without measuring?

  The measured numbers (Apple M1, 8 cores, `bench_test.go`'s 95/5 read/write
  mix over disjoint per-goroutine key ranges — the same shape as the real
  workers): `RWMutex` + `atomic.Int64` at ~136-152 ns/op, 2 B/op, 0
  allocs/op, versus `sync.Map` at ~160-164 ns/op, 5 B/op, 0 allocs/op —
  `sync.Map` about 15% slower on the exact workload its own doc comment
  describes.

  The doc comment isn't wrong, it's incomplete without the constants.
  `sync.Map`'s fast path — a `Load` on a key already promoted into the
  read-only map — really is close to free: one atomic pointer read, no
  mutex. But that fast path only fully pays off once the key set has
  *stabilized* and writes have essentially stopped. This workload never
  gets there: every `StoreInCache` on a new or updated key, and every
  `InvalidateCacheEntry` delete, has to go through the mutex-guarded dirty
  map (and, on a miss against the read map, promotion bookkeeping) — and at
  a 95/5 ratio there's a steady trickle of exactly that traffic, forever.
  Layer on `sync.Map`'s `any`-typed `Load`/`Store` API, which costs an
  interface conversion per entry (matches the extra `B/op` on `sync.Map`
  above; `RWMutex`'s access is direct and unboxed), and the constant-factor
  overhead outweighs what the lock-free read path saves at this contention
  level. None of that is visible from the doc comment's description of
  *access pattern* — it only shows up in the doc comment's fine print about
  *implementation* (dirty map, mutex, promotion) and, ultimately, in a
  benchmark. "Matches the documented use case" earns a workload the right to
  be *tried* against `sync.Map`; it doesn't earn `sync.Map` the win without
  measuring, and hardware/contention differences mean even this table is a
  reason to re-run the benchmark, not cite it.

## Common pitfalls

- **One giant `sync.Mutex` on `Service`.** Silences every warning, throws
  away all read concurrency on the cache. Correct, but a bad answer — call
  it out and push for two separate locks.
- **Reaching for `sync.Map` for the metrics map.** It's designed for
  read-mostly workloads with stable keys — the opposite of a hot metrics
  counter. Slower and uglier here.
- **Locking `GetFromCache` with `RLock` and calling it done.** The
  `HitCount++` race is under `RLock` — `-race` still fires. Students often
  need a nudge back to the "write at 41 / read at 69" report.
- **Locking `RecordMetric` inside `GetFromCache` while holding
  `cacheMu.RLock`.** Two locks, always in the same order, so no deadlock
  here — but flag lock ordering to preempt questions when they see it.
- **Reentrant lock inside `GetCacheStats`.** If someone extracts
  `result.HitCount.Load()` behind a helper that also takes `cacheMu.RLock`,
  they'll hang. `sync.RWMutex.RLock` is *not* recursive across an
  intervening `Lock` — the Go docs are explicit; mention it.
- **Assuming `sync.Map` must be faster because the doc comment describes
  your workload.** The bonus benchmark in walkthrough step 6 shows the
  opposite here. "Matches the documented use case" is a reason to
  benchmark, not a reason to skip benchmarking.
