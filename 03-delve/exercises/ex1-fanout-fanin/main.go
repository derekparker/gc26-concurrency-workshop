// Log-ingest pipeline: a fan-out/fan-in event processor.
//
// Events are fanned out to a pool of workers, each worker classifies the
// event and records statistics, and results are fanned back in to a single
// collector that produces the final report.
//
// SYMPTOM: the program processes a few dozen events and then stops making
// progress. The monitor keeps printing the same count forever. It never
// crashes, it never finishes. Your job is to find out why -- with Delve,
// not by reading every line of this file.
package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // debug endpoint, only served when INGEST_DEBUG_ADDR is set
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Severity levels for classified events.
const (
	SevInfo = iota
	SevWarning
	SevError
	SevCritical
)

// Event is a raw log event entering the pipeline.
type Event struct {
	ID  int
	Msg string
}

// Result is a classified event leaving the pipeline.
type Result struct {
	EventID  int
	Severity int
	WorkerID int
}

// Stats records aggregate statistics about classified events.
// All methods are safe for concurrent use.
type Stats struct {
	mu          sync.Mutex
	bySeverity  [4]int
	criticalIDs []int
}

// Record updates the aggregate counts for a classified event.
func (s *Stats) Record(eventID, severity int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.bySeverity[severity]++
	if severity == SevCritical {
		s.noteCritical(eventID)
	}
}

// noteCritical remembers critical event IDs so the report can list them.
func (s *Stats) noteCritical(eventID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.criticalIDs = append(s.criticalIDs, eventID)
}

// Processed returns the total number of events recorded so far.
func (s *Stats) Processed() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := 0
	for _, n := range s.bySeverity {
		total += n
	}
	return total
}

// classify assigns a severity to an event based on its message.
func classify(msg string) int {
	switch {
	case strings.HasPrefix(msg, "FATAL"):
		return SevCritical
	case strings.HasPrefix(msg, "ERROR"):
		return SevError
	case strings.HasPrefix(msg, "WARN"):
		return SevWarning
	default:
		return SevInfo
	}
}

// worker fans in events, classifies them, and reports results.
func worker(id int, events <-chan Event, results chan<- Result, stats *Stats, wg *sync.WaitGroup) {
	defer wg.Done()

	for ev := range events {
		// Simulate parsing/classification work.
		time.Sleep(5 * time.Millisecond)

		sev := classify(ev.Msg)
		stats.Record(ev.ID, sev)

		results <- Result{EventID: ev.ID, Severity: sev, WorkerID: id}
	}
}

// generateEvents produces a deterministic stream of log events.
// Every run of the program sees exactly the same events.
func generateEvents(count int) []Event {
	events := make([]Event, count)
	for i := range count {
		msg := fmt.Sprintf("INFO request %d handled", i)
		switch {
		case i%37 == 36:
			msg = fmt.Sprintf("FATAL disk failure on shard %d", i%5)
		case i%11 == 10:
			msg = fmt.Sprintf("ERROR upstream timeout for request %d", i)
		case i%7 == 6:
			msg = fmt.Sprintf("WARN slow response for request %d", i)
		}
		events[i] = Event{ID: i, Msg: msg}
	}
	return events
}

func main() {
	log.SetFlags(log.Lmicroseconds)

	// Production style: expose /debug/pprof when asked to. Used by the
	// goroutine leak profile stretch goal in the README; harmless otherwise.
	if addr := os.Getenv("INGEST_DEBUG_ADDR"); addr != "" {
		go func() { log.Println(http.ListenAndServe(addr, nil)) }()
	}

	const (
		numWorkers = 8
		numEvents  = 200
	)

	events := make(chan Event)
	results := make(chan Result, numWorkers)
	stats := &Stats{}

	// Fan-out: start the worker pool.
	var wg sync.WaitGroup
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go worker(i, events, results, stats, &wg)
	}

	// Close results once every worker has exited.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Feed the pipeline.
	go func() {
		for _, ev := range generateEvents(numEvents) {
			events <- ev
		}
		close(events)
	}()

	// Progress monitor: prints throughput so you can see the stall happen.
	// It reads an atomic counter (not Stats) so it can never fall behind
	// the pipeline.
	var collectedSoFar atomic.Int64
	go func() {
		for {
			time.Sleep(2 * time.Second)
			log.Printf("[MONITOR] collected %d/%d results", collectedSoFar.Load(), numEvents)
		}
	}()

	// Fan-in: collect results until the results channel is closed.
	collected := 0
	for range results {
		collected++
		collectedSoFar.Store(int64(collected))
	}

	log.Printf("collected %d results", collected)
	log.Printf("severity counts: INFO=%d WARN=%d ERROR=%d FATAL=%d",
		stats.bySeverity[SevInfo], stats.bySeverity[SevWarning],
		stats.bySeverity[SevError], stats.bySeverity[SevCritical])
	if collected != numEvents {
		log.Printf("FAILED: expected %d results", numEvents)
		os.Exit(1)
	}
	log.Println("SUCCESS")
}
