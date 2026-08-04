// Package analyzer distills a Go execution trace into a compact diagnosis:
// per-goroutine-group time in each scheduling state (running / runnable /
// blocked / syscall), the source locations that blocked the most, and the
// longest individual scheduler waits.
//
// It is deliberately small: one pass over the event stream with a tiny state
// machine per goroutine. The heavy lifting — decoding, validating, and
// time-ordering the binary trace — is done by golang.org/x/exp/trace.
// The same code works on traces from trace.Start, go test -trace, the
// net/http/pprof endpoint, and flight recorder snapshots.
package analyzer

import (
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/exp/trace"
)

// minSchedWait filters scheduler-wait noise: runnable intervals shorter than
// this still count toward the Runnable bucket but are not reported as
// individual events.
const minSchedWait = time.Millisecond

// maxReported caps the number of blocking sites and scheduler-wait events
// kept in a Summary, so the JSON stays compact even for huge traces.
const maxReported = 50

// Analyze reads a Go execution trace from r and returns a Summary.
//
// This is the provided skeleton: it iterates over every event, keeps the
// per-goroutine bookkeeping up to date, and delegates the actual accounting
// to the three record* functions below — the ones you implement.
func Analyze(r io.Reader) (*Summary, error) {
	tr, err := trace.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("reading trace: %w", err)
	}
	a := &analysis{
		gs:     make(map[trace.GoID]*gInfo),
		blocks: make(map[blockKey]*blockAgg),
	}
	for {
		ev, err := tr.ReadEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading trace: %w", err)
		}
		if ev.Kind() != trace.EventStateTransition {
			continue // we only care about scheduling state changes
		}
		a.observe(ev.Time())
		st := ev.StateTransition()
		if st.Resource.Kind != trace.ResourceGoroutine {
			continue // skip proc state transitions
		}
		from, to := st.Goroutine()
		g := a.goroutine(st.Resource.Goroutine(), ev.Time())
		g.noteStack(st.Stack)
		if from == to {
			// Not a real transition: the tracer re-asserts every
			// goroutine's state at generation boundaries (~1/sec).
			// Skipping these keeps g.since equal to the time the
			// goroutine truly entered its current state, so long
			// waits aren't chopped into per-generation segments.
			continue
		}

		// Order matters here: the two "an interval just ended" recorders
		// read g.since — the time g entered the `from` state — before
		// recordStateTime advances it to ev.Time().
		a.recordSchedWait(g, from, to, ev.Time())
		a.recordBlocking(g, from, to, ev.Time(), st)
		a.recordStateTime(g, from, ev.Time())
		g.state = to
	}
	a.flush()
	return a.summary(), nil
}

// AnalyzeFile opens the trace file at p and analyzes it.
func AnalyzeFile(p string) (*Summary, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Analyze(f)
}

// ───────────────────────── your part starts here ─────────────────────────

// TODO 1: recordStateTime charges the time g spent in its previous state
// (from g.since to ts) to that state's bucket, then advances g.since to ts.
//
// Use bucket(from) to map the state to an index into g.buckets; it returns
// -1 for states we don't account for (undetermined / not yet existing) —
// skip the charge for those, but still advance g.since.
func (a *analysis) recordStateTime(g *gInfo, from trace.GoState, ts trace.Time) {
	if b := bucket(from); b >= 0 {
		g.buckets[b] += ts.Sub(g.since)
	}
	g.since = ts
}

// TODO 2: recordBlocking aggregates blocked time by blocking site.
//
// Two edges matter:
//   - going to sleep (to == GoWaiting, from != GoWaiting): remember why and
//     where in g.blockReason and g.blockSite. The transition carries both:
//     st.Reason is human-readable ("chan receive", "sync", "select", ...)
//     and siteOf(st.Stack) is the blocking source location.
//   - waking up (from == GoWaiting, to != GoWaiting): the whole wait —
//     ts.Sub(g.since) — is credited to a.blocks[blockKey{site, reason}]:
//     bump count, add to total, raise max. Allocate the *blockAgg on first
//     use, and clear g.blockSite/g.blockReason when done.
func (a *analysis) recordBlocking(g *gInfo, from, to trace.GoState, ts trace.Time, st trace.StateTransition) {
	if to == trace.GoWaiting && from != trace.GoWaiting {
		g.blockReason = st.Reason
		g.blockSite = siteOf(st.Stack)
		return
	}
	if from == trace.GoWaiting && to != trace.GoWaiting && g.blockSite != "" {
		d := ts.Sub(g.since)
		k := blockKey{site: g.blockSite, reason: g.blockReason}
		b := a.blocks[k]
		if b == nil {
			b = &blockAgg{}
			a.blocks[k] = b
		}
		b.count++
		b.total += d
		b.max = max(b.max, d)
		g.blockSite, g.blockReason = "", ""
	}
}

// TODO 3: recordSchedWait records scheduler latency. A Runnable→Running edge
// ends a scheduler wait that began at g.since; its length is ts.Sub(g.since).
//
// Raise g.maxSchedWait if this wait beats it, and if the wait is at least
// minSchedWait, append a schedWaitEv{g, g.since, wait} to a.schedWaits.
func (a *analysis) recordSchedWait(g *gInfo, from, to trace.GoState, ts trace.Time) {
	if from != trace.GoRunnable || to != trace.GoRunning {
		return
	}
	wait := ts.Sub(g.since)
	g.maxSchedWait = max(g.maxSchedWait, wait)
	if wait >= minSchedWait {
		a.schedWaits = append(a.schedWaits, schedWaitEv{g: g, at: g.since, wait: wait})
	}
}

// ────────────────────────── your part ends here ──────────────────────────

// The four buckets we charge a goroutine's time to.
type bucketID int

const (
	bRunning  bucketID = iota // executing on a P
	bRunnable                 // ready to run, waiting for a P: scheduler latency
	bBlocked                  // parked: channels, locks, select, sleep, I/O
	bSyscall                  // in a system call
	numBuckets
)

// bucket maps a goroutine state to an accounting bucket, or -1 for states we
// don't account for. New GoStates may be added in future Go releases, so
// treat anything unknown as -1.
func bucket(s trace.GoState) bucketID {
	switch s {
	case trace.GoRunning:
		return bRunning
	case trace.GoRunnable:
		return bRunnable
	case trace.GoWaiting:
		return bBlocked
	case trace.GoSyscall:
		return bSyscall
	}
	return -1
}

// gInfo is the per-goroutine state machine.
type gInfo struct {
	id           trace.GoID
	startFunc    string        // outermost frame: what the goroutine was started with
	state        trace.GoState // current state
	since        trace.Time    // when it entered that state
	buckets      [numBuckets]time.Duration
	maxSchedWait time.Duration

	// Set on the transition into GoWaiting, consumed on the way out.
	blockReason string
	blockSite   string
}

// noteStack names the goroutine after the outermost frame of its own stack —
// the function it was started with. This is the same grouping as the trace
// viewer's "Goroutine analysis" page.
func (g *gInfo) noteStack(stk trace.Stack) {
	if g.startFunc != "" {
		return
	}
	for f := range stk.Frames() {
		g.startFunc = f.Func // frames iterate leaf→root; last one wins
	}
}

type blockKey struct{ site, reason string }

type blockAgg struct {
	count      int
	total, max time.Duration
}

type schedWaitEv struct {
	g    *gInfo
	at   trace.Time
	wait time.Duration
}

// analysis accumulates state during the single pass over the trace.
type analysis struct {
	gs         map[trace.GoID]*gInfo
	blocks     map[blockKey]*blockAgg
	schedWaits []schedWaitEv
	start, end trace.Time
	sawTime    bool
}

// observe tracks the first and last event timestamps (events arrive in time
// order from the reader).
func (a *analysis) observe(ts trace.Time) {
	if !a.sawTime {
		a.start, a.sawTime = ts, true
	}
	if ts > a.end {
		a.end = ts
	}
}

// goroutine returns the state machine for id, creating it on first sight.
func (a *analysis) goroutine(id trace.GoID, ts trace.Time) *gInfo {
	g := a.gs[id]
	if g == nil {
		g = &gInfo{id: id, state: trace.GoUndetermined, since: ts}
		a.gs[id] = g
	}
	return g
}

// flush charges each goroutine's final, still-open interval, up to the last
// event in the trace.
func (a *analysis) flush() {
	for _, g := range a.gs {
		a.recordStateTime(g, g.state, a.end)
	}
}

// siteOf returns the innermost non-runtime frame of stk, e.g.
// "main.heartbeat (main.go:85)" — the source location that blocked.
func siteOf(stk trace.Stack) string {
	fallback := "(no stack)"
	first := true
	for f := range stk.Frames() {
		if first {
			fallback, first = frameString(f), false
		}
		if !strings.HasPrefix(f.Func, "runtime.") && !strings.HasPrefix(f.Func, "internal/") {
			return frameString(f)
		}
	}
	return fallback
}

func frameString(f trace.StackFrame) string {
	return fmt.Sprintf("%s (%s:%d)", f.Func, path.Base(f.File), f.Line)
}

// summary converts the accumulated state into the exported Summary.
func (a *analysis) summary() *Summary {
	s := &Summary{
		TraceDuration: Duration(a.end.Sub(a.start)),
		Goroutines:    len(a.gs),
	}

	// Group goroutines by start function.
	groups := make(map[string]*Group)
	for _, g := range a.gs {
		name := g.startFunc
		if name == "" {
			name = "(unknown)"
		}
		gr := groups[name]
		if gr == nil {
			gr = &Group{StartFunc: name}
			groups[name] = gr
		}
		gr.Count++
		gr.Running += Duration(g.buckets[bRunning])
		gr.Runnable += Duration(g.buckets[bRunnable])
		gr.Blocked += Duration(g.buckets[bBlocked])
		gr.Syscall += Duration(g.buckets[bSyscall])
		gr.MaxSchedWait = max(gr.MaxSchedWait, Duration(g.maxSchedWait))
	}
	for _, gr := range groups {
		s.Groups = append(s.Groups, *gr)
	}
	slices.SortFunc(s.Groups, func(x, y Group) int {
		return int(y.Total() - x.Total()) // descending by total time
	})

	// Blocking sites, worst first.
	for k, b := range a.blocks {
		s.TopBlocking = append(s.TopBlocking, BlockSite{
			Site:   k.site,
			Reason: k.reason,
			Count:  b.count,
			Total:  Duration(b.total),
			Max:    Duration(b.max),
		})
	}
	slices.SortFunc(s.TopBlocking, func(x, y BlockSite) int {
		return int(y.Total - x.Total)
	})
	s.TopBlocking = truncate(s.TopBlocking, maxReported)

	// Longest individual scheduler waits.
	slices.SortFunc(a.schedWaits, func(x, y schedWaitEv) int {
		return int(y.wait - x.wait)
	})
	for _, w := range truncate(a.schedWaits, maxReported) {
		name := w.g.startFunc
		if name == "" {
			name = "(unknown)"
		}
		s.LongestSchedWaits = append(s.LongestSchedWaits, SchedWait{
			Goroutine: int64(w.g.id),
			StartFunc: name,
			At:        Duration(w.at.Sub(a.start)),
			Wait:      Duration(w.wait),
		})
	}
	return s
}

func truncate[S ~[]E, E any](s S, n int) S {
	if len(s) > n {
		return s[:n]
	}
	return s
}
