# 02-execution-tracer: Seeing What Your Program Actually Did

## Overview
This section teaches the Go execution tracer: the runtime's flight-data
recorder. While the race detector (section 01) finds memory races and Delve
(section 03) lets you inspect a stopped program, the execution tracer answers
a different question: **where did the time actually go?** It records every
scheduling event in your program, goroutines starting, stopping, blocking,
waiting to be scheduled, entering syscalls, GC pauses, with nanosecond
timestamps, and gives you a timeline you can scrub through.

Logs tell you what your code chose to print. CPU profiles tell you where CPU
time was spent, but nothing about the time spent *not* running. The tracer
shows the gaps: scheduling latency, goroutines stuck on channels and mutexes,
hidden serialization, GC stop-the-world pauses. Every exercise in this section
is a bug where the logs look vague ("it's slow sometimes") and the trace makes
the cause visually obvious.

One more reason this matters in 2026: as AI coding agents write more of our
code, engineers spend more of their time on architecture, deployment, and
debugging. An execution trace is exactly the kind of ground truth that neither
a human nor an AI agent can get by reading source, and a trace file (or a
flight recorder snapshot from production) is concrete evidence you can hand to
an agent to reason from, instead of letting it guess.

## What You'll Learn
- How to capture traces: `trace.Start`, `go test -trace`, `net/http/pprof`,
  and the flight recorder
- How to read the trace viewer: procs, goroutine states, blocking, GC
- Goroutine analysis and the derived profiles (sync block, scheduler latency)
- Annotating your own code with tasks, regions, and logs
- Capturing rare production events with `runtime/trace.FlightRecorder`

## Schedule (~90 minutes)
| Part | Time | What |
|------|------|------|
| [demo/](demo/) | ~25 min | Presenter-led tour of `go tool trace` + tasks/regions/logs |
| [exercises/ex1-scheduling](exercises/ex1-scheduling/) | ~20 min | Scheduler starvation: tail latency from too many runnable goroutines |
| [exercises/ex2-flightrecorder](exercises/ex2-flightrecorder/) | ~25 min | Flight recorder: catch a rare latency spike in a long-running service |
| [exercises/ex3-io](exercises/ex3-io/) | ~20 min | Hidden serialization: a "parallel" pipeline that runs one record at a time |
| [exercises/ex4-traceagent](exercises/ex4-traceagent/) | bonus, ~20–25 min | **Capstone (if time allows, or homework):** parse traces programmatically with the reader API, then expose your analyzer to an AI agent as an MCP server |

## Four Ways to Get a Trace

```go
// 1. Whole program: bracket the code you care about.
f, _ := os.Create("out.trace")
trace.Start(f)
defer trace.Stop()
```

```bash
# 2. Tests and benchmarks (one package at a time — -trace rejects multiple):
go test -trace=trace.out .
```

```go
// 3. Live services: net/http/pprof exposes a trace endpoint.
import _ "net/http/pprof"
// then: curl -o trace.out 'http://localhost:8080/debug/pprof/trace?seconds=5'
```

```go
// 4. Rare events in production (Go 1.25+): the flight recorder keeps the
// last few seconds in a ring buffer; snapshot when something goes wrong.
fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{
    MinAge:   5 * time.Second,
    MaxBytes: 16 << 20,
})
fr.Start()
// ... on anomaly: fr.WriteTo(f)
```

Overhead is no longer a reason to avoid any of these: since Go 1.21 tracing
costs roughly 1–2% CPU for typical programs (down from 10–20% in older
releases), and Go 1.22 reworked the trace format so traces stream and scale.
The flight recorder became official API in Go 1.25.

## Reading the Trace Viewer

```bash
go tool trace <file>.trace
```

This starts a web server. The landing page links to:

- **View trace by proc**, the main timeline. One row per `GOMAXPROCS`
  logical processor showing which goroutine ran on it at every instant, plus
  rows for goroutine/heap/thread counts and GC. Navigate with the keyboard:
  `w`/`s` zoom, `a`/`d` pan; click any slice to see its stack and statistics.
  The viewer is Chromium-based, use Chrome for this page.
- **Goroutine analysis** (`/goroutines`), goroutines grouped by start
  function, with per-group breakdowns: Execution time, Block time (chan
  send/recv, select, sync, syscall), **Sched wait time**, and more.
- **Profiles**, pprof-style graphs derived from the trace: network blocking,
  synchronization blocking, syscall, and **scheduler latency**. Also available
  headless (types: `net`, `sync`, `syscall`, `sched`):
  `go tool trace -pprof=sync out.trace > sync.pprof && go tool pprof -top sync.pprof`.
  (If you open one in pprof's own web UI, note that Go 1.26 changed its default
  view to the flame graph; the old graph view is under View → Graph.)
- **User-defined tasks / regions**, your own annotations (see the demo).

The one table to internalize, goroutine states on the timeline:

| State | Meaning | If you see a lot of it |
|-------|---------|------------------------|
| Running | executing on a P | fine, this is the goal |
| **Runnable** | ready to run, waiting for a P | **scheduler starvation**: too much runnable work (ex1) |
| Blocked (chan/sync/select) | parked on a channel or lock | contention or serialization (ex2, ex3) |
| Network wait / syscall | parked in the netpoller or a syscall | slow I/O, or normal, if it's supposed to wait |
| GC / STW | garbage collector activity | allocation pressure; look at the Heap row |

One note on that last row: Go 1.26 enabled the **Green Tea** garbage collector
by default (an opt-in experiment in 1.25). It improves marking and scanning of
small objects through better locality and CPU scalability, worth roughly a
10–40% reduction in GC overhead for programs that lean on the collector, plus
another ~10% on newer amd64 (Intel Ice Lake / AMD Zen 4 and up) where it uses
vector instructions. Practical effect here: GC rows in a fresh trace are
lighter than in traces you captured a year ago. Don't diagnose a 2024 GC
problem from a 2026 timeline.

A good habit for every exercise: **capture a trace before your fix and after,
and compare them side by side.** The before/after diff of the timeline is the
most convincing artifact you can attach to a PR, or feed to an AI agent.

## Monitoring for This Without a Trace (Go 1.26)

A trace is something you have to decide to capture, in advance, on the machine
having the problem. Go 1.26 added scheduler metrics to
[`runtime/metrics`](https://pkg.go.dev/runtime/metrics) that let you watch for
the same conditions continuously:

| Metric | What it counts |
|--------|----------------|
| `/sched/goroutines/runnable:goroutines` | ready to execute, but not executing |
| `/sched/goroutines/running:goroutines` | executing (≤ `/sched/gomaxprocs`) |
| `/sched/goroutines/waiting:goroutines` | waiting on I/O or a sync primitive |
| `/sched/goroutines/not-in-go:goroutines` | in a syscall or cgo call |
| `/sched/goroutines-created:goroutines` | total created since program start |
| `/sched/threads/total:threads` | live runtime-owned threads |

Pair them with `/sched/latencies:seconds` (the distribution of time goroutines
spend runnable before actually running, available since Go 1.20). A rising
`runnable` count and a fattening latency histogram *are* exercise 1's bug,
visible without capturing anything. The counts are approximate and aren't
guaranteed to sum to `/sched/goroutines:goroutines`.

This is the same division of labor the workshop uses elsewhere: the cheap
always-on signal tells you **that** something is wrong and roughly where; the
expensive on-demand tool (a trace, or a flight recorder snapshot) tells you
**why**. Put `runnable` and `/sched/latencies` on a dashboard, alert on them,
and capture a trace when they fire.

One related change worth knowing, since exercise 1's fix is "bound concurrency
to roughly `GOMAXPROCS`": since **Go 1.25**, `GOMAXPROCS` on Linux respects the
cgroup CPU bandwidth limit (the Kubernetes "CPU limit", not "CPU requests"),
and on all platforms the runtime periodically re-checks it. A worker pool sized
once at startup can therefore be wrong later. Both behaviors switch off if
`GOMAXPROCS` is set explicitly, or via the `containermaxprocs=0` /
`updatemaxprocs=0` GODEBUG settings.

## Requirements
- Go 1.25+ (Go 1.26 recommended, these modules declare `go 1.26`); the flight
  recorder exercise needs 1.25 at minimum
- Chrome/Chromium for the timeline view
- **graphviz** (`dot`) for the `/io`, `/block`, `/syscall`, and `/sched`
  graph pages — the trace viewer shells out to it. Without graphviz those
  pages fail; the `-pprof=<kind>` + `go tool pprof -top` path used
  throughout these notes needs nothing extra and is the safe fallback.
- Network access once for the ex4 capstone (its first build downloads 8
  modules, ~49 MB: `golang.org/x/exp/trace`, the MCP SDK, and their
  dependencies); ex4's agent stage optionally uses Claude Code

## Looking Ahead: Go 1.27

Expected August 2026. `runtime/trace` itself is unchanged, the capture APIs and
the flight recorder work exactly as described above. What changes around them:

- **The goroutine leak profile goes generally available.** The
  `GOEXPERIMENT=goroutineleakprofile` build-time flag is deleted and
  `/debug/pprof/goroutineleak` is simply present. Part III uses it as a stretch
  goal against a real stall,
  [see it there](../03-delve/exercises/ex1-fanout-fanin/README.md#stretch-the-goroutine-leak-profile).
- **Tracebacks carry pprof goroutine labels.** For modules declaring `go 1.27`
  or later, the traceback header line includes `runtime/pprof` labels (disable
  with the `tracebacklabels=0` GODEBUG). These are the same labels Delve groups
  goroutines by in Part III, now present in every panic and stack dump.
- **`go tool trace -http` binds localhost only** when given just a port
  (`-http=:6060`), matching `go tool pprof`. Pass an explicit address
  (`-http=0.0.0.0:6060`) to listen on all interfaces, which matters if you run
  the viewer on a remote box.

## Additional Resources
- [runtime/trace package docs](https://pkg.go.dev/runtime/trace)
- [More powerful Go execution traces (Go blog, 2024)](https://go.dev/blog/execution-traces-2024)
- [Flight Recorder in Go 1.25 (Go blog)](https://go.dev/blog/flight-recorder)
- [golang.org/x/exp/trace](https://pkg.go.dev/golang.org/x/exp/trace), the
  trace reader API for parsing traces programmatically, that's
  [exercises/ex4-traceagent](exercises/ex4-traceagent/), the bonus capstone,
  where you build your own automated trace analysis and hand it to an AI
  agent over MCP
