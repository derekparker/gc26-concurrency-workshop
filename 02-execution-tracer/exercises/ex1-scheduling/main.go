package main

import (
	"fmt"
	"log"
	"os"
	"runtime/trace"
	"slices"
	"sync"
	"time"
)

// This little "service" does two things at once:
//
//  1. Crunches a big backlog of analytics batches (CPU-bound work).
//  2. Sends a heartbeat every 10ms. In production, missing a few
//     heartbeats in a row gets the instance killed by the orchestrator.
//
// The ops team reports the service is "randomly getting restarted under
// load". CPU profiles just show batch work (which is expected!), and the
// logs only show the symptom, printed at exit below: heartbeat p99/max
// latency is way over the 10ms target.
//
// Your job: use the execution tracer to find out WHY the heartbeat
// goroutine can't run on time, then fix it. See README.md.

const (
	numBatches      = 1000
	heartbeatPeriod = 10 * time.Millisecond
)

func main() {
	f, err := os.Create("scheduling.trace")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := trace.Start(f); err != nil {
		log.Fatal(err)
	}
	defer trace.Stop()

	// Start the heartbeat before the batch work begins.
	stop := make(chan struct{})
	intervalsCh := make(chan []time.Duration, 1)
	go heartbeat(stop, intervalsCh)

	// Process the backlog.
	start := time.Now()
	var wg sync.WaitGroup
	for i := range numBatches {
		wg.Go(func() {
			processBatch(i)
		})
	}
	wg.Wait()
	elapsed := time.Since(start)

	close(stop)
	intervals := <-intervalsCh

	slices.Sort(intervals)
	fmt.Printf("processed %d batches in %v\n", numBatches, elapsed)
	if n := len(intervals); n > 0 {
		fmt.Printf("heartbeat interval: target %v | p50 %v | p99 %v | max %v\n",
			heartbeatPeriod,
			intervals[n/2].Round(time.Millisecond),
			intervals[n*99/100].Round(time.Millisecond),
			intervals[n-1].Round(time.Millisecond))
	}
}

// heartbeat records the observed time between ticks and sends the recorded
// intervals on out when stop is closed. If the scheduler runs this goroutine
// promptly, every interval is ~10ms.
func heartbeat(stop <-chan struct{}, out chan<- []time.Duration) {
	var intervals []time.Duration
	ticker := time.NewTicker(heartbeatPeriod)
	defer ticker.Stop()

	last := time.Now()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			intervals = append(intervals, now.Sub(last))
			last = now
		case <-stop:
			out <- intervals
			return
		}
	}
}

// processBatch simulates CPU-bound work on one batch (~a few ms).
func processBatch(id int) {
	result := float64(id)
	for i := range 4_000_000 {
		result = result*1.000001 + float64(i%7)
		result = result / 1.0000005
	}
	// Use the result so the compiler can't remove the loop.
	if result == -1 {
		fmt.Println("impossible", result)
	}
}
