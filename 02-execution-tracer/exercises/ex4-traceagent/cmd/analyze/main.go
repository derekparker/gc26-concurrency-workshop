// Command analyze summarizes a Go execution trace: per-goroutine-group time
// in each scheduling state, top blocking sites, and the longest scheduler
// waits. Works on any Go 1.22+ trace, including flight recorder snapshots.
//
// Usage:
//
//	go run ./cmd/analyze [-json] [-top N] file.trace
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"traceagent/analyzer"
)

func main() {
	log.SetFlags(0)
	jsonOut := flag.Bool("json", false, "emit JSON instead of a human-readable report")
	topN := flag.Int("top", 10, "rows to show per section (human-readable output)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: analyze [-json] [-top N] file.trace\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	s, err := analyzer.AnalyzeFile(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	if *jsonOut {
		out, err := s.JSON()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(out)
		return
	}
	fmt.Print(s.Render(*topN))
}
