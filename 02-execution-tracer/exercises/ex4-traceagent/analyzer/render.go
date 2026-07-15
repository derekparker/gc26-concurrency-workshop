package analyzer

// The Summary types and rendering. All provided — nothing to complete here.

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

// Summary is the analyzer's output: everything an engineer — or an AI agent —
// needs to form a scheduling-level diagnosis, in a few hundred bytes instead
// of a multi-megabyte trace.
type Summary struct {
	TraceDuration     Duration    `json:"trace_duration"`
	Goroutines        int         `json:"goroutines"`
	Groups            []Group     `json:"groups"`
	TopBlocking       []BlockSite `json:"top_blocking,omitempty"`
	LongestSchedWaits []SchedWait `json:"longest_sched_waits,omitempty"`
}

// Group aggregates all goroutines that share a start function — the same
// grouping as the trace viewer's "Goroutine analysis" page.
type Group struct {
	StartFunc    string   `json:"start_func"`
	Count        int      `json:"count"`
	Running      Duration `json:"running"`
	Runnable     Duration `json:"runnable"` // ready to run, waiting for a P
	Blocked      Duration `json:"blocked"`
	Syscall      Duration `json:"syscall"`
	MaxSchedWait Duration `json:"max_sched_wait"` // longest single runnable interval
}

// Total is the group's cumulative time across all buckets (roughly: lifetime
// summed over all goroutines in the group).
func (g Group) Total() Duration {
	return g.Running + g.Runnable + g.Blocked + g.Syscall
}

// BlockSite is a source location where goroutines blocked, with the runtime's
// wait reason ("chan receive", "sync", "select", ...).
type BlockSite struct {
	Site   string   `json:"site"`
	Reason string   `json:"reason"`
	Count  int      `json:"count"`
	Total  Duration `json:"total"`
	Max    Duration `json:"max"`
}

// SchedWait is one goroutine's single wait between becoming runnable and
// actually running — scheduler latency, the time where tail latency hides.
type SchedWait struct {
	Goroutine int64    `json:"goroutine"`
	StartFunc string   `json:"start_func"`
	At        Duration `json:"at"` // offset from the start of the trace
	Wait      Duration `json:"wait"`
}

// Duration is a time.Duration that JSON-marshals as a human-readable string
// ("1.31s") instead of a nanosecond count — friendlier to both humans and
// language models.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// String renders the duration rounded to a readable precision.
func (d Duration) String() string {
	v := time.Duration(d)
	switch {
	case v == 0:
		return "0"
	case v >= time.Second:
		return v.Round(10 * time.Millisecond).String()
	case v >= time.Millisecond:
		return v.Round(10 * time.Microsecond).String()
	default:
		return v.Round(time.Microsecond).String()
	}
}

// JSON renders the summary as indented JSON.
func (s *Summary) JSON() (string, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Render renders the summary as a human-readable report, showing at most
// topN rows per section.
func (s *Summary) Render(topN int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "trace duration: %v | goroutines: %d | groups: %d\n",
		s.TraceDuration, s.Goroutines, len(s.Groups))

	fmt.Fprintf(&sb, "\nGOROUTINE GROUPS (by start function, top %d by total time)\n", topN)
	w := tabwriter.NewWriter(&sb, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "START FUNC\tCOUNT\tRUNNING\tRUNNABLE\tBLOCKED\tSYSCALL\tMAX SCHED WAIT")
	for _, g := range truncate(s.Groups, topN) {
		fmt.Fprintf(w, "%s\t%d\t%v\t%v\t%v\t%v\t%v\n",
			g.StartFunc, g.Count, g.Running, g.Runnable, g.Blocked, g.Syscall, g.MaxSchedWait)
	}
	w.Flush()

	fmt.Fprintf(&sb, "\nTOP BLOCKING SITES (by total blocked time, top %d)\n", topN)
	w = tabwriter.NewWriter(&sb, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SITE\tREASON\tCOUNT\tTOTAL\tMAX")
	for _, b := range truncate(s.TopBlocking, topN) {
		reason := b.Reason
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%v\t%v\n", b.Site, reason, b.Count, b.Total, b.Max)
	}
	w.Flush()

	fmt.Fprintf(&sb, "\nLONGEST SCHEDULER WAITS (single runnable→running gaps, top %d)\n", topN)
	if len(s.LongestSchedWaits) == 0 {
		fmt.Fprintln(&sb, "(none over 1ms — the scheduler is keeping up)")
		return sb.String()
	}
	w = tabwriter.NewWriter(&sb, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WAIT\tAT\tGOROUTINE\tSTART FUNC")
	for _, sw := range truncate(s.LongestSchedWaits, topN) {
		fmt.Fprintf(w, "%v\t+%v\tG%d\t%s\n", sw.Wait, sw.At, sw.Goroutine, sw.StartFunc)
	}
	w.Flush()
	return sb.String()
}
