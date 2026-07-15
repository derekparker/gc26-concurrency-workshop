package main

import (
	"fmt"
	"sync"
	"time"
)

// Stats tracks processing statistics shared by all workers.
type Stats struct {
	processed   int
	workers     int
	startTime   time.Time
	lastUpdated time.Time
}

// NewStats creates a statistics tracker.
func NewStats() *Stats {
	now := time.Now()
	return &Stats{
		startTime:   now,
		lastUpdated: now,
	}
}

// RecordWork updates the statistics for completed work.
func (s *Stats) RecordWork(items int) {
	s.processed += items
	s.lastUpdated = time.Now()
}

// RegisterWorker tracks active workers.
func (s *Stats) RegisterWorker() {
	s.workers++
	s.lastUpdated = time.Now()
}

// GetTotal returns the total processed items.
func (s *Stats) GetTotal() int {
	return s.processed
}

// GetWorkerCount returns the number of registered workers.
func (s *Stats) GetWorkerCount() int {
	return s.workers
}

// GetElapsedTime returns time since stats creation.
func (s *Stats) GetElapsedTime() time.Duration {
	return time.Since(s.startTime)
}

// GetLastUpdated returns the last update time.
func (s *Stats) GetLastUpdated() time.Time {
	return s.lastUpdated
}

// GetTimeSinceUpdate returns duration since last update.
func (s *Stats) GetTimeSinceUpdate() time.Duration {
	return time.Since(s.lastUpdated)
}

// IsStale checks if stats haven't been updated recently.
func (s *Stats) IsStale() bool {
	return s.GetTimeSinceUpdate() > 100*time.Millisecond
}

// processItems simulates a worker processing items in batches.
func processItems(id int, stats *Stats, wg *sync.WaitGroup) {
	defer wg.Done()

	stats.RegisterWorker()

	const batches = 10
	const batchSize = 100

	for batch := range batches {
		// Do the "work" for this batch locally...
		count := 0
		for range batchSize {
			count++ // simulate per-item work
		}

		// ...then publish the result to the shared stats.
		stats.RecordWork(count)

		// Report progress halfway through.
		if batch == batches/2 {
			fmt.Printf("Worker %d halfway: %d items so far (last update %v ago)\n",
				id, stats.GetTotal(), stats.GetTimeSinceUpdate().Round(time.Microsecond))
		}

		// Simulate time between batches (I/O, network, etc.).
		time.Sleep(20 * time.Millisecond)
	}
}

// monitorProgress periodically reports the current progress.
func monitorProgress(stats *Stats, done chan bool) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			total := stats.GetTotal()
			workers := stats.GetWorkerCount()
			elapsed := stats.GetElapsedTime()

			rate := float64(total) / elapsed.Seconds()

			status := "active"
			if stats.IsStale() {
				status = "STALE"
			}

			fmt.Printf("[Monitor] %s - %d items by %d workers (%.0f items/sec)\n",
				status, total, workers, rate)

		case <-done:
			return
		}
	}
}

func main() {
	stats := NewStats()
	var wg sync.WaitGroup

	numWorkers := 5

	fmt.Printf("Starting %d workers...\n", numWorkers)

	// Start progress monitor.
	monitorDone := make(chan bool)
	go monitorProgress(stats, monitorDone)

	// Start workers.
	for i := range numWorkers {
		wg.Add(1)
		go processItems(i, stats, &wg)
	}

	wg.Wait()

	// Stop monitor.
	monitorDone <- true

	// Final statistics.
	fmt.Printf("\nProcessing complete!\n")
	fmt.Printf("Workers used: %d (expected: %d)\n", stats.GetWorkerCount(), numWorkers)
	fmt.Printf("Total items processed: %d (expected: 5000)\n", stats.GetTotal())
	fmt.Printf("Total time: %v\n", stats.GetElapsedTime().Round(time.Millisecond))
}
