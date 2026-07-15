package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/trace"
	"sync"
	"time"
)

// The nightly "record shipper": download 200 records from an internal API,
// compress each one, and append it to an archive file.
//
// The previous owner made downloading concurrent — 8 workers fetch records
// in parallel — and left this note:
//
//	"Should take ~1s now (200 records / 8 workers x 30ms per fetch), but
//	 it still takes over 5 seconds?! Tried 16 and 32 workers: NO change.
//	 The API team swears it isn't them. I give up."
//
// Adding workers doesn't help. CPU profiles show compression, but the
// machine is 90% idle while this runs. Logs show nothing slow. Your job:
// use the execution tracer to find where the time actually goes, then fix
// it. See README.md.

const (
	numRecords = 200
	numWorkers = 8
	fetchDelay = 30 * time.Millisecond // simulated API latency
)

type record struct {
	id   int
	body []byte
}

func main() {
	// A local stand-in for the internal API.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(fetchDelay)
		fmt.Fprintf(w, "payload for record %s", r.URL.Query().Get("id"))
	}))
	defer api.Close()

	f, err := os.Create("io.trace")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := trace.Start(f); err != nil {
		log.Fatal(err)
	}
	defer trace.Stop()

	ctx, task := trace.NewTask(context.Background(), "shipRecords")
	defer task.End()

	archive, err := os.Create("archive.out")
	if err != nil {
		log.Fatal(err)
	}
	defer archive.Close()

	start := time.Now()

	jobs := make(chan int)
	results := make(chan record)

	// Fan out: 8 workers fetch records concurrently.
	var workers sync.WaitGroup
	workers.Add(numWorkers)
	for range numWorkers {
		go worker(ctx, api.URL, jobs, results, &workers)
	}

	// Fan in: a collector appends each record to the archive.
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)
	go collector(ctx, archive, results, &collectorWG)

	for id := range numRecords {
		jobs <- id
	}
	close(jobs)
	workers.Wait()
	close(results)
	collectorWG.Wait()

	elapsed := time.Since(start)
	fmt.Printf("shipped %d records in %v (%.1f records/sec)\n",
		numRecords, elapsed.Round(time.Millisecond),
		float64(numRecords)/elapsed.Seconds())
	expected := numRecords / numWorkers * fetchDelay
	fmt.Printf("expected ~%v with %d workers... where does the time go?\n", expected, numWorkers)
}

// worker downloads each record it's assigned and passes it on for
// collection.
func worker(ctx context.Context, apiURL string, jobs <-chan int, results chan<- record, wg *sync.WaitGroup) {
	defer wg.Done()
	for id := range jobs {
		var rec record
		trace.WithRegion(ctx, "fetch", func() {
			rec = fetch(apiURL, id)
		})
		trace.WithRegion(ctx, "send result", func() {
			results <- rec
		})
	}
}

// collector compresses each record and appends it to the archive.
// Compression is CPU work, ~25ms per record.
func collector(ctx context.Context, archive *os.File, results <-chan record, wg *sync.WaitGroup) {
	defer wg.Done()
	for rec := range results {
		var compressed []byte
		trace.WithRegion(ctx, "compress", func() {
			compressed = compress(rec)
		})
		trace.WithRegion(ctx, "append to archive", func() {
			if _, err := archive.Write(compressed); err != nil {
				log.Fatal(err)
			}
		})
	}
}

func fetch(apiURL string, id int) record {
	resp, err := http.Get(fmt.Sprintf("%s/?id=%d", apiURL, id))
	if err != nil {
		log.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Fatal(err)
	}
	return record{id: id, body: body}
}

// compress squeezes a record down for the archive. It's pure CPU work that
// takes ~25ms per record.
func compress(rec record) []byte {
	spin(25 * time.Millisecond)
	return fmt.Appendf(nil, "compressed(%d bytes) record %d\n", len(rec.body), rec.id)
}

// spin simulates CPU-bound work for roughly duration d.
func spin(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}
