# Exercise 2: The Flaky Service (~15 min)

## The Situation

A service keeps two maps with very different access patterns:

1. **Cache** (`map[string]*CachedResult`), expensive results, computed once,
   read many times, rarely invalidated.
2. **Metrics** (`map[string]int`), counters incremented constantly, read
   occasionally for reporting.

The team's bug tracker has a ticket titled *"CI job crashes sometimes,
cannot reproduce."* Run it a few times:

```bash
go run .   # repeat 5-10 times
```

Most runs finish cleanly:

```
=== Final Statistics ===
Cache entries: 62
Overall cache hit rate: 93.2% (929 hits, 68 misses, 9 invalidations)
Total operations recorded: 634
```

But every few runs:

```
fatal error: concurrent map writes

goroutine 10 [running]:
internal/runtime/maps.fatal(...)
main.(*Service).StoreInCache(...)
      .../ex2-map/main.go:52
main.cacheWorker(0x2, ...)
      .../ex2-map/main.go:127 +0x2c0
```

(You may also see `concurrent map read and map write` or `concurrent map
iteration and map write`, same disease, different symptom.)

## Notice What the Crash Doesn't Tell You

The fatal error gives you **one** goroutine, the one that lost the coin
toss. Who was the *other* writer? Which map operation? No idea. It's also
best-effort: the runtime's map check only fires when two accesses physically
collide, which is why CI is flaky instead of red. It is not the race
detector, it's a cheap tripwire that happens to catch some races on maps
only.

## Your Task

1. Get the full story:

   ```bash
   go run -race .
   ```

   Now you get *pairs* of stacks, every run — though the map tripwire can
   still kill the run early, before the summary; if that happens, re-run.
   Inventory the races,
   you should find them on **both maps**, and on something that is *not* a
   map (look for a report pointing inside `GetFromCache`).

2. Fix all races. Requirements:
   - `go run -race .` → zero warnings, and no more fatal crashes, ever.
   - Cache hit rate should stay high (~90%+); output otherwise similar.
3. The two maps have different read/write mixes. Choose synchronization
   **per map**, and be ready to justify the choice.

## Questions to Discuss

- Why does the plain run only crash *sometimes*, while `-race` complains
  *every* time? (What does the detector track that the map tripwire doesn't?)
- `GetAllMetrics` copies the map before returning it. Copying is a *read*,
  why is it still racy?
- The race on `CachedResult.HitCount` survives even if you lock every map
  operation perfectly. Why?

<details>
<summary><strong>Hint 1</strong> (the inventory)</summary>

Four distinct problems:

1. `cache` map: written in `StoreInCache`/`InvalidateCacheEntry`, read in
   `GetFromCache`, iterated in `GetCacheStats`, all unsynchronized.
2. `metrics` map: `RecordMetric` does `m[k]++` from six goroutines;
   `GetMetric`/`GetAllMetrics` read concurrently.
3. `CachedResult.HitCount++` in `GetFromCache` mutates the *struct behind
   the pointer*. Map locking doesn't protect what the values point to.
4. `GetCacheStats` also reads `HitCount` of every entry while workers
   increment them.

</details>

<details>
<summary><strong>Solution sketch</strong></summary>

**Metrics map, plain `sync.Mutex`.** The workload is ~90% writes;
`RWMutex` buys nothing when everyone needs the write lock, and `sync.Map` is
explicitly *not* designed for write-heavy counters:

```go
type Service struct {
	cacheMu sync.RWMutex
	cache   map[string]*CachedResult

	metricsMu sync.Mutex
	metrics   map[string]int
}

func (s *Service) RecordMetric(metric string) {
	s.metricsMu.Lock()
	s.metrics[metric]++
	s.metricsMu.Unlock()
}
```

**Cache map, `sync.RWMutex`.** Read-mostly with stable keys: many
concurrent `RLock` readers, exclusive `Lock` for store/invalidate.

**The trap:** `HitCount++` is a *write* that happens on the cache's *read*
path. If `GetFromCache` takes only `RLock`, `-race` still fires on
`HitCount`. Options:

- make `HitCount` an `atomic.Int64` (fine under `RLock`), or
- take the full `Lock` in `GetFromCache` (kills read concurrency), or
- drop per-entry hit counts and count hits in metrics instead.

The atomic field is the idiomatic fix, a nice example of mixing a lock
(for the map) with an atomic (for one hot field).

Expect the build to break once you change the type: `verifier` also reads
`HitCount` directly (`main.go:251` and `:259`) and comparing two
`atomic.Int64` values won't compile. Use `.Load()` in both places. That
compile error is a feature, changing a field to an atomic changes its
contract, and the compiler hands you every caller that has to know.

**`sync.Map`?** Reasonable-looking for the cache (stable keys, read-mostly,
exactly its documented use case), and it would remove the `RWMutex`. But
it's `any`-typed, doesn't fix `HitCount` on its own (that field still needs
`atomic.Int64` regardless of the map type), and is wrong for the
write-heavy metrics map. `bench_test.go` in this directory benchmarks both:
on the machine this was written on, `sync.Map` came out ~15% *slower* than
`RWMutex` for this workload, despite matching the documented use case.
Worth running yourself and worth discussing — not required, and not a free
win just because the docs describe your access pattern.

Two use cases, two different answers, that's the point of this exercise.

</details>
