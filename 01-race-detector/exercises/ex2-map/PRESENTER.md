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
     everyone needs the write lock. `sync.Map` is explicitly documented as
     the wrong tool for write-heavy counters. Plain `sync.Mutex`.
   - `cache` map: read-mostly, keys stable. Many concurrent readers, rare
     writers. `sync.RWMutex` is the shape.

5. **The trap that survives every lock choice (2 min).**
   `HitCount++` is a *write* that lives on the cache's *read* path
   (`GetFromCache` under `RLock`). Under `RWMutex.RLock`, N readers run
   concurrently and all mutate `HitCount` — the detector still fires.
   Three options; the atomic wins:

   - Make `HitCount` an `atomic.Int64` — fine under `RLock`, no blocking.
   - Take the full `Lock` in `GetFromCache` — kills read concurrency; the
     whole point of `RWMutex` gone.
   - Drop per-entry hit counts, use the metrics map for hits.

## Fix

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

## Ask the room

- Why does the plain run only crash *sometimes*, while `-race` complains
  *every* time? What does the detector track that the map tripwire doesn't?
- `GetAllMetrics` uses `maps.Copy` to snapshot the map. Copying is a *read*
  operation — why is it still racy against writers?
- The race on `CachedResult.HitCount` survives even if you lock every map
  operation perfectly. Where does that race live, and why is a lock on the
  *map* the wrong tool for it?
- Is `sync.Map` a fit for either of these maps? For which one, and why not
  the other?

## Common pitfalls

- **One giant `sync.Mutex` on `Service`.** Silences every warning, throws
  away all read concurrency on the cache. Correct, but a bad answer — call
  it out and push for two separate locks.
- **Reaching for `sync.Map` for the metrics map.** It's designed for
  read-mostly workloads with stable keys — the opposite of a hot metrics
  counter. Slower and uglier here.
- **Locking `GetFromCache` with `RLock` and calling it done.** The
  `HitCount++` race is under `RLock` — `-race` still fires. Students often
  need a nudge back to the "read at 41 / read at 69" report.
- **Locking `RecordMetric` inside `GetFromCache` while holding
  `cacheMu.RLock`.** Two locks, always in the same order, so no deadlock
  here — but flag lock ordering to preempt questions when they see it.
- **Reentrant lock inside `GetCacheStats`.** If someone extracts
  `result.HitCount.Load()` behind a helper that also takes `cacheMu.RLock`,
  they'll hang. `sync.RWMutex.RLock` is *not* recursive across an
  intervening `Lock` — the Go docs are explicit; mention it.
