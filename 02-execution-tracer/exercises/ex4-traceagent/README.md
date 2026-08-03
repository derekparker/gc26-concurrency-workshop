# Exercise 4 (bonus): Build a Trace Analyzer Your AI Agent Can Use (~20–25 min)

*Capstone, do it if time allows, or take it home. Everything here builds on
ex1; nothing later depends on it.*

## The Idea
In ex1 you diagnosed scheduler starvation **by eye**: you opened the trace
viewer, clicked through Goroutine analysis, and found `main.heartbeat` with
seconds of sched-wait time. That worked because you knew where to look.

The trace viewer is not the only consumer of a trace. The trace format has a
supported reader API, [`golang.org/x/exp/trace`](https://pkg.go.dev/golang.org/x/exp/trace),
the same decoder that powers `go tool trace`, so anything you learned to spot
by eye, you can compute. This exercise does that in two stages:

1. **Stage 1 (~15 min):** finish a small trace analyzer that replays every
   goroutine's state machine and emits the diagnosis as a compact report:
   time per scheduling state by goroutine group, top blocking sites, longest
   scheduler waits.
2. **Stage 2 (~5–10 min):** hand that capability to an AI agent. A pre-built
   MCP server (using the official
   [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)) wraps your
   analyzer so Claude Code can call it on any trace file, including flight
   recorder snapshots from production (ex2).

This is the workshop's AI-era thesis made concrete: an agent can't attach a
debugger to your incident and shouldn't guess from source. A trace is ground
truth, but a raw multi-MB binary trace doesn't fit in a context window.
**You** decide what the trace gets distilled into, and the agent reasons from
that.

## Setup
You need a "bad" trace from ex1. If you didn't keep `broken.trace`, make one
(traces are gitignored, everyone generates their own):

```bash
cd ../ex1-scheduling
go run main.go      # repeat until p99 is embarrassing (>300ms)
cp scheduling.trace broken.trace
cd ../ex4-traceagent
```

> If the analyzer (or `go tool trace`) reports a parse error on your
> trace — `expected batch event, got event 0` or similar — the trace
> itself is bad, not your code. It happens to roughly 1 run in 25.
> Delete it and generate another.

Use the **broken** ex1 program (goroutine-per-batch), if you already fixed
it, revert your fix first; the whole point is to re-find that bug. The first
build downloads 8 modules (~49 MB — `golang.org/x/exp`, the MCP SDK and
its dependencies), so this exercise needs network once.

## Stage 1: Programmatic Trace Analysis

Layout, you only touch one file:

```
analyzer/analyzer.go   the event loop (provided) + 3 TODO functions (yours)
analyzer/render.go     output types + JSON/table rendering (provided)
cmd/analyze/           the CLI (provided)
cmd/mcp/               the MCP server for stage 2 (provided)
```

The skeleton already reads the trace with `trace.NewReader`, filters for
goroutine `EventStateTransition` events, and maintains a `gInfo` state machine
per goroutine (`g.state`, and `g.since`, when it entered that state). Run it
as-is:

```bash
go run ./cmd/analyze ../ex1-scheduling/broken.trace
```

You'll get the goroutine groups with correct counts and all-zero durations,
the bookkeeping runs, but nobody does the accounting. That's your job, three
functions in `analyzer/analyzer.go`:

1. **TODO 1, `recordStateTime`**: charge `ts - g.since` to the bucket for
   the state the goroutine is leaving, advance `g.since`. This one function
   reproduces the trace viewer's Execution / Block / Sched-wait columns.
2. **TODO 2, `recordBlocking`**: on the edge *into* `GoWaiting`, capture the
   wait reason (`st.Reason`) and blocking site (`siteOf(st.Stack)`); on the
   edge *out*, credit the whole wait to that site in `a.blocks`.
3. **TODO 3, `recordSchedWait`**: a `GoRunnable → GoRunning` edge ends a
   scheduler wait that started at `g.since`. Track the per-goroutine max and
   collect the individual waits ≥ 1ms.

Each function has a doc comment telling you exactly what to do. When you're
done:

```bash
go run ./cmd/analyze ../ex1-scheduling/broken.trace
```

Real output from a real broken run (p99 was 525ms, max 587ms):

```text
trace duration: 3s | goroutines: 1010 | groups: 11

GOROUTINE GROUPS (by start function, top 10 by total time)
START FUNC                                           COUNT  RUNNING  RUNNABLE  BLOCKED  SYSCALL  MAX SCHED WAIT
sync.(*WaitGroup).Go.func1                           1000   23.92s   31m37.5s  0        0        2.97s
main.main                                            1      989µs    4µs       2.99s    9µs      3µs
runtime.traceStartReadCPU.func1                      1      137µs    27µs      3s       0        8µs
runtime.(*traceAdvancerState).start.func1            1      1.81ms   10.44ms   2.98s    0        10.42ms
runtime/trace.(*traceMultiplexer).startLocked.func1  1      42µs     10.43ms   2.98s    453µs    10.43ms
main.heartbeat                                       1      403µs    1.07s     1.92s    0        562.93ms
runtime.forcegchelper                                1      0        0         1.98s    0        0
...

TOP BLOCKING SITES (by total blocked time, top 10)
SITE                                       REASON        COUNT  TOTAL  MAX
runtime.chanrecv1 (chan.go:509)            chan receive  29     4.99s  1s
sync.(*WaitGroup).Wait (waitgroup.go:206)  sync          1      2.99s  2.99s
...
main.heartbeat (main.go:84)                select        101    1.92s  35.33ms

LONGEST SCHEDULER WAITS (single runnable→running gaps, top 10)
WAIT   AT      GOROUTINE  START FUNC
2.97s  +869µs  G1004      sync.(*WaitGroup).Go.func1
2.97s  +871µs  G1007      sync.(*WaitGroup).Go.func1
...
```

That's the ex1 diagnosis, computed instead of clicked:

- `sync.(*WaitGroup).Go.func1` × **1000**, with **31.6 cumulative minutes**
  runnable against 24s running, a thousand goroutines spending 98% of their
  existence waiting in the run queue. Some waited **2.97s for their first
  slice** (bottom table).
- `main.heartbeat`: 403µs of actual work, **1.07s runnable**, max single
  sched wait **562.93ms**, against a 10ms deadline. The select-block time
  (1.92s, max 35ms per wait) is the legitimate part, waiting for ticks.
- Sanity check it yourself: open `go tool trace broken.trace` → Goroutine
  analysis. The numbers match to within microseconds — same decoder, same
  events.

There's also `-json` (same content, machine-shaped, this is what feeds the
MCP tool) and `-top N`.

<details>
<summary>Hint (if the numbers come out wrong)</summary>

Two classic mistakes:

1. **Ordering.** `recordSchedWait` and `recordBlocking` must read `g.since`
   *before* `recordStateTime` advances it, the provided call order in
   `Analyze` already does this; keep your logic inside the right function.
2. **Wrong edge.** Blocked time ends on the `GoWaiting → *` edge, not on the
   next time the goroutine runs, a woken goroutine is *runnable* first, and
   that gap is scheduler latency, not blocking. Keeping those two separate is
   the entire reason ex1's bug was invisible in CPU profiles.

Also note the skeleton skips `from == to` transitions for you: the tracer
re-asserts every goroutine's state at generation boundaries (~1/sec), and if
you treat those as real transitions, a long scheduler wait gets chopped at
those boundaries — a 3.3s wait reports as 1.0s. (Ask us how we know.)
</details>

<details>
<summary>Solution (all three TODOs)</summary>

```go
func (a *analysis) recordStateTime(g *gInfo, from trace.GoState, ts trace.Time) {
	if b := bucket(from); b >= 0 {
		g.buckets[b] += ts.Sub(g.since)
	}
	g.since = ts
}

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
```
</details>

## Stage 2: Hand It to an Agent

> **Stage 2 needs a finished Stage 1.** Both MCP tools are thin wrappers
> over `analyzer.AnalyzeFile`, so against the un-completed TODOs they
> return an empty report (`top 10 blocking sites (of 0)`) and the agent has
> nothing to reason about. If you skipped Stage 1, or want a known-good
> baseline to demo from, apply the solution:
>
> ```bash
> git apply solution.diff        # from this directory
> ```
>
> Undo it with `git apply -R solution.diff` when you want the TODOs back.

`cmd/mcp` is provided complete (~100 lines, read it, it's mostly tool
descriptions). It wraps your analyzer in an MCP server over stdio, exposing
two tools: `analyze_trace(path, format)` and `top_blocking(path, n)`.

Wire it into Claude Code and try it:

```bash
# from this directory:
claude mcp add gotrace -- go -C "$(pwd)" run ./cmd/mcp

claude   # then paste a prompt like:
```

> Here's an execution trace from a Go service with terrible tail latency:
> `<absolute path>/02-execution-tracer/exercises/ex1-scheduling/broken.trace`.
> Use the gotrace tools to diagnose it. What is wrong, and what would you
> change in the code?

Watch what happens: the agent calls `analyze_trace`, sees the 1000-goroutine
group with minutes of runnable time next to a starved single goroutine, and
names scheduler starvation, because you handed it the same evidence you used
in ex1, in a form it can actually consume. (`claude mcp remove gotrace` when
you're done; `claude mcp list` should show `gotrace: ✓ connected`.)

No Claude Code? The server is just JSON-RPC on stdio, you can drive it by
hand, which is also how you debug any MCP server:

```bash
{
  echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"me","version":"0"}}}'
  echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 2
} | go run ./cmd/mcp
```

### What the agent can, and can't, conclude
Be honest about what you just built. From the summary an agent can identify
the *class* of problem (starvation vs. contention vs. serialization vs. slow
I/O), the goroutines involved, the blocking sites, and the magnitudes, which
is enough to point at code and propose the ex1 worker-pool fix. It **cannot**
see event ordering, task/region annotations, GC phases, or anything your
summarizer didn't compute: if the answer requires "what happened in the 40ms
*before* the spike", the summary won't say. The raw trace remains the ground
truth; the summary is a lossy projection you chose. When the agent's answer
seems too confident, that's the thing to check, and adding a sharper tool
(say, `timeline(path, goroutine)`) is a `mcp.AddTool` call away.

### Production tie-in
The analyzer reads any Go 1.22+ trace, including **flight recorder snapshots**
(ex2), nothing in the reader API cares how the trace was captured. On an ex2
snapshot, `top_blocking` surfaces the lock convoy directly:

```text
sync.(*RWMutex).RLock (rwmutex.go:74)   sync   8   2.11s   264.8ms
```

Eight workers, ~265ms each, the refresher's write lock, quantified. An agent
with these two tools plus a flight recorder snapshot from an incident ticket
can do the ex2 investigation end to end.

## What This Teaches
`go tool trace` is one consumer of the trace format, not the trace format.
With the reader API, "I saw it in the viewer" becomes "we compute it in CI",
"the incident bot attaches it to the ticket", and "the agent diagnosed the
snapshot", the same 40 lines of state machine you wrote today. Diagnostic
artifacts beat vibes, for humans and for agents alike; the engineers who
decide *what the artifact gets distilled into* are the ones the AI era needs.
