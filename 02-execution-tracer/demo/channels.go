package main

import (
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

// A tiny two-goroutine pipeline: a sender fetches messages from an HTTP API
// and pushes them down a channel, and a receiver consumes them.
//
//	go run channels.go
//	go tool trace channel.trace
func main() {
	// A local stand-in for a remote API. Each request takes ~20ms.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintf(w, "response for id=%s", r.URL.Query().Get("id"))
	}))
	defer api.Close()

	f, err := os.Create("channel.trace")
	if err != nil {
		log.Fatal("Error creating trace file:", err)
	}
	defer f.Close()

	trace.Start(f)
	defer trace.Stop()

	var wg sync.WaitGroup
	ch := make(chan string, 5)

	wg.Add(2)
	go sender(api.URL, ch, &wg)
	go receiver(ch, &wg)

	wg.Wait()
}

func sender(apiURL string, ch chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	for i := range 5 {
		resp, err := http.Get(fmt.Sprintf("%s/?id=%d", apiURL, i))
		if err != nil {
			log.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Fatal(err)
		}

		ch <- fmt.Sprintf("message %d: %s", i, body)
	}
	close(ch)
}

func receiver(ch chan string, wg *sync.WaitGroup) {
	defer wg.Done()

	for msg := range ch {
		fmt.Printf("Received: %q\n", msg)
	}
}
