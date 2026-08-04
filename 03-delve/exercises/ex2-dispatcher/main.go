// Build dispatcher: hands compilation jobs to a small worker pool and
// gathers the build reports.
//
// The channels are buffered "generously" for a typical burst of jobs, and
// the whole thing worked fine in the smoke test (which used 4 jobs).
//
// SYMPTOM: with a realistic batch of 12 jobs the program crashes almost
// immediately with:
//
//	fatal error: all goroutines are asleep - deadlock!
//
// Run it under Delve instead: the fatal throw becomes a breakpoint, and the
// frozen process will tell you exactly who is stuck on which channel.
package main

import (
	"fmt"
	"log"
	"time"
)

// Job is a unit of work for the pool.
type Job struct {
	ID     int
	Target string
}

// Report is the outcome of one job.
type Report struct {
	JobID    int
	Target   string
	Duration time.Duration
	OK       bool
}

// worker consumes jobs and produces reports until the jobs channel closes.
func worker(id int, jobs <-chan Job, reports chan<- Report) {
	for job := range jobs {
		start := time.Now()

		// Simulate the build.
		time.Sleep(10 * time.Millisecond)

		reports <- Report{
			JobID:    job.ID,
			Target:   job.Target,
			Duration: time.Since(start),
			OK:       true,
		}
	}
}

func makeJobs(count int) []Job {
	jobs := make([]Job, count)
	for i := range count {
		jobs[i] = Job{ID: i + 1, Target: fmt.Sprintf("pkg/service%02d", i+1)}
	}
	return jobs
}

func main() {
	log.SetFlags(log.Lmicroseconds)

	const numWorkers = 3

	batch := makeJobs(12)

	// Buffers sized for a "typical" burst; matched the 4-job smoke test.
	jobs := make(chan Job, 4)
	reports := make(chan Report, 2)

	for i := 1; i <= numWorkers; i++ {
		go worker(i, jobs, reports)
	}

	// Phase 1: dispatch the whole batch.
	log.Printf("dispatching %d jobs", len(batch))
	for _, job := range batch {
		jobs <- job
		log.Printf("dispatched job %d (%s)", job.ID, job.Target)
	}
	close(jobs)

	// Phase 2: gather all the reports.
	log.Printf("gathering reports")
	for range batch {
		r := <-reports
		log.Printf("job %d finished in %s", r.JobID, r.Duration)
	}

	log.Println("build complete")
}
