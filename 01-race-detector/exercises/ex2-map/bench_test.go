package main

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
)

// BenchmarkCache drives GetFromCache/StoreInCache concurrently at roughly the
// same 95% read / 5% write ratio cacheWorker uses, with each goroutine
// confined to its own disjoint key range - mirroring the real workload's
// per-worker key ownership. Run after atomic.diff (RWMutex) and again after
// syncmap.diff (sync.Map) and compare:
//
//	go test -bench=. -benchmem -run=^$
//
// Each goroutine gets its own rand.Source so the benchmark isn't itself
// bottlenecked on math/rand's global lock.
func BenchmarkCache(b *testing.B) {
	const keysPerWorker = 20

	svc := NewService()

	b.ResetTimer()

	var workerID atomic.Int32
	b.RunParallel(func(pb *testing.PB) {
		id := workerID.Add(1) - 1

		keys := make([]string, keysPerWorker)
		for i := range keys {
			keys[i] = fmt.Sprintf("worker_%d_key_%d", id, i)
			svc.StoreInCache(keys[i], "warm")
		}

		rng := rand.New(rand.NewSource(int64(id) + 1))

		for pb.Next() {
			key := keys[rng.Intn(keysPerWorker)]
			if rng.Float32() < 0.95 {
				svc.GetFromCache(key)
			} else {
				svc.StoreInCache(key, "recomputed")
			}
		}
	})
}
