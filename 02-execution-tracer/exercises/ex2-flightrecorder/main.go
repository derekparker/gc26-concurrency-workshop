package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"runtime/trace"
	"sync"
	"time"
)

// A long-running "profile lookup service", condensed into one file. Eight
// workers serve a steady stream of requests from an in-memory cache. The
// SLO says p99 < 50ms; the dashboard says requests normally finish in well
// under a millisecond.
//
// The on-call report: "a couple of times an hour, a burst of requests takes
// 250ms+. We can't reproduce it, and by the time we see it on the dashboard
// it's long over."
//
// You can't run trace.Start for an hour and dig through gigabytes of trace.
// This is exactly what runtime/trace.FlightRecorder (official API since Go
// 1.25) is for: keep the last few seconds of trace in a ring buffer, and
// snapshot it AT THE MOMENT a slow request is detected.
//
// Your job (see README.md):
//   1. Create and start a FlightRecorder (TODO 1).
//   2. When a request breaches slowThreshold, write a snapshot to
//      flightrecorder.trace — exactly once (TODO 2).
//   3. Open the snapshot in `go tool trace` and explain the latency spike.

const (
	runFor        = 12 * time.Second
	numWorkers    = 8
	cacheEntries  = 50_000
	slowThreshold = 100 * time.Millisecond
)

// TODO 1: declare a *trace.FlightRecorder here (package scope is fine for
// this exercise), create it in main with a FlightRecorderConfig — pick a
// MinAge and MaxBytes — and Start() it before the workers launch.
//
// https://pkg.go.dev/runtime/trace#FlightRecorder

type service struct {
	mu    sync.RWMutex
	cache map[int]string
}

// handle serves one request: look up an entry and render a response.
func (s *service) handle(id int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.cache[id%cacheEntries]
	spin(200 * time.Microsecond) // render the response
	return v
}

// refresh keeps the cache warm. Most refreshes are incremental and cheap;
// every fourth one is a full revalidation.
func (s *service) refresh(ctx context.Context, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trace.WithRegion(ctx, "refresh cache", func() {
		if n%4 != 3 {
			// Incremental: touch a few entries.
			for i := range 100 {
				s.cache[rand.IntN(cacheEntries)] = fmt.Sprintf("value-%d-%d", n, i)
			}
			return
		}
		// Full revalidation: rebuild and verify every entry.
		next := make(map[int]string, cacheEntries)
		for i := range cacheEntries {
			next[i] = fmt.Sprintf("value-%d-%d", n, i)
		}
		spin(250 * time.Millisecond) // "verify" the rebuilt entries
		s.cache = next
	})
}

// refresher runs cache refreshes in the background until stop is closed.
func (s *service) refresher(ctx context.Context, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for n := 0; ; n++ {
		select {
		case <-ticker.C:
			s.refresh(ctx, n)
		case <-stop:
			return
		}
	}
}

// worker serves requests until stop is closed, reporting each request's
// latency on the latencies channel.
func (s *service) worker(ctx context.Context, w int, stop <-chan struct{}, latencies chan<- time.Duration, wg *sync.WaitGroup) {
	defer wg.Done()
	ctx, task := trace.NewTask(ctx, fmt.Sprintf("worker-%d", w))
	defer task.End()
	for {
		select {
		case <-stop:
			return
		default:
		}

		time.Sleep(time.Duration(1+rand.IntN(4)) * time.Millisecond)

		start := time.Now()
		trace.WithRegion(ctx, "handle request", func() {
			s.handle(rand.IntN(cacheEntries))
		})
		elapsed := time.Since(start)
		latencies <- elapsed

		if elapsed > slowThreshold {
			log.Printf("SLOW request: %v (threshold %v)", elapsed, slowThreshold)
			// TODO 2: this is the "rare event just happened" moment.
			// Snapshot the flight recorder to flightrecorder.trace —
			// exactly once (sync.Once), and in a new goroutine so the
			// worker isn't stalled while the snapshot is written.
		}
	}
}

func main() {
	svc := &service{cache: make(map[int]string, cacheEntries)}
	ctx := context.Background()
	svc.refresh(ctx, 3) // initial full load

	stop := make(chan struct{})
	latencies := make(chan time.Duration, 1024)

	var bg sync.WaitGroup
	bg.Add(1 + numWorkers)
	go svc.refresher(ctx, stop, &bg)
	for w := range numWorkers {
		go svc.worker(ctx, w, stop, latencies, &bg)
	}

	// Per-second latency report: this is all the visibility the "dashboard"
	// gives you. Note how vague it is compared to what the trace will show.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var window []time.Duration
		for {
			select {
			case l := <-latencies:
				window = append(window, l)
			case <-ticker.C:
				fmt.Println(summarize(window))
				window = window[:0]
			case <-stop:
				return
			}
		}
	}()

	time.Sleep(runFor)
	close(stop)
	bg.Wait()
	fmt.Println("done")
}

func summarize(window []time.Duration) string {
	if len(window) == 0 {
		return "no requests"
	}
	var sum, max time.Duration
	for _, l := range window {
		sum += l
		if l > max {
			max = l
		}
	}
	return fmt.Sprintf("%4d requests | avg %8v | max %8v",
		len(window),
		(sum / time.Duration(len(window))).Round(10*time.Microsecond),
		max.Round(10*time.Microsecond))
}

// spin simulates CPU-bound work for roughly duration d.
func spin(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}
