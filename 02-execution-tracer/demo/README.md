# Demo: A First Trace, Then Tasks & Regions (presenter walkthrough)

## Overview
A deliberately tiny program: a `sender` goroutine fetches five messages from
an HTTP API (a local `httptest` server with ~20ms latency — no conference
wifi required) and pushes them down a buffered channel; a `receiver` consumes
them. No bug to find. The goal is to get everyone comfortable **opening,
navigating, and reading a trace**, then to show how `runtime/trace`
annotations (tasks, regions, logs) turn an anonymous timeline into *your*
program's story.

Total time: **~25 minutes** (Part 1 ~12 min, Part 2 ~10 min, questions ~3 min).

## Part 1: Capture and Tour a Raw Trace (~12 min)

```bash
cd 02-execution-tracer/demo
go run channels.go
go tool trace channel.trace
```

(`channel.trace` in this directory is a pre-generated backup in case the live
run misbehaves; running the program overwrites it.)

Walk through `channels.go` first — it's ~70 lines. Point at `trace.Start(f)` /
`defer trace.Stop()`: that's the entire integration cost. Mention the other
capture methods (`go test -trace`, `/debug/pprof/trace`, flight recorder —
coming in exercise 2).

`go tool trace` opens a landing page in the browser. Tour it top to bottom:

### 1. "View trace by proc" — the timeline
- **What students should see:** one row per logical processor (PROCS), mostly
  *empty*. Five thin bursts of activity spaced ~20ms apart — one per HTTP
  request — with `net/http` internals and our goroutines flickering in and
  out. Above the procs: the Goroutines, Heap, and Threads counter rows.
- Teach the keyboard immediately, it's the whole game: `w`/`s` zoom (zoom
  into one of the five bursts), `a`/`d` pan, click a slice to see its stack
  trace and duration in the bottom panel.
- Click a `main.sender` slice: bottom panel shows the call stack. Ask the
  room: "the program ran for ~150ms — why is almost every pixel idle?"
  Answer: it's waiting on the network, and *waiting leaves almost no trace on
  a CPU timeline*. Segue: the tracer knows exactly what it was waiting on.

### 2. "Goroutine analysis" (`/goroutines`)
- Table of goroutines grouped by start location: `main.sender`,
  `main.receiver`, `main.main`, plus `net/http` internals.
- Click `main.sender`. **What students should see:** a breakdown row where
  Execution time is tiny (hundreds of µs) and the dominant column is network
  wait / syscall — the 5×20ms of HTTP. Point out the per-group profile links
  (Network wait, Sync block, Syscall, Scheduler wait).
- Click `main.receiver`: dominated by "Block time (chan receive)" — it spent
  its life parked on `<-ch`. Emphasize: **the tracer accounts for time spent
  doing nothing**, which is precisely what profilers can't do.

### 3. The derived profiles (mention, don't dwell)
- From the landing page: Network blocking / Synchronization blocking /
  Syscall / Scheduler latency profiles — pprof graphs computed from the
  trace. The exercises use them; here just show they exist.

## Part 2: Tasks, Regions, and Logs (~10 min)

Motivate: in the raw trace we had to *recognize* our goroutines by function
name and guess what "iteration 3" was. The `runtime/trace` annotation API
lets the program label its own work.

Apply the prepared diff (from the **repo root**):

```bash
git apply 02-execution-tracer/demo/tasks.diff
```

Show the diff quickly (`git diff`), calling out the three APIs:
- `ctx, task := trace.NewTask(ctx, "requestAndSend")` + `defer task.End()` —
  one logical *unit of work*, potentially spanning many goroutines. The ctx
  carries the task; everything created from it is grouped under it.
- `trace.WithRegion(ctx, "http request", func() {...})` — brackets a code
  interval *within one goroutine* (regions must begin and end in the same
  goroutine; tasks are the cross-goroutine construct).
- `trace.Log(ctx, "sending request", "3")` — a timestamped key/value pinned
  to the task.

Re-run and re-open:

```bash
cd 02-execution-tracer/demo
go run channels.go
go tool trace channel.trace
```

### "User-defined tasks" (`/usertasks`)
- **What students should see:** a table — Task type `requestAndSend`,
  Count 1, and a duration histogram (~150–200ms bucket).
- Click the count/bucket link to open the task list, then look at the single
  task's event log: a chronological table (When | Elapsed | Goroutine |
  Events) interleaving *both goroutines*: `task "requestAndSend" begin`,
  `region "receive loop" begin` (G of receiver), `log "0"`, `region "http
  request" begin/end` (~20ms apart), `region "channel send" begin/end`
  (microseconds — the buffer absorbs it), repeating five times.
- This table alone answers "what did request 3 do, and when?" — per-request
  latency accounting with ~10 lines of annotation.
- Click **(goroutine view)** on the task: the timeline again, but filtered to
  the task, with region bars drawn under the goroutines.

### "User-defined regions" (`/userregions`)
- Region types with counts and duration histograms: `http request` ×5
  (~20ms each), `channel send` ×5 (~µs), `receive loop` ×1 (~whole run).
  Note the http transport's own unnamed regions in the list too — the
  standard library annotates some of itself.

Reset for the next run of the demo:

```bash
git apply -R 02-execution-tracer/demo/tasks.diff
```

## Anticipated Questions
- **"What does this cost in production?"** ~1–2% CPU since Go 1.21. The
  bigger cost is trace *volume* (MB/s on busy services) — which is what the
  flight recorder solves (exercise 2).
- **"Can a region span goroutines?"** No — regions are per-goroutine by
  design. Use a task and pass its ctx; start a region in each goroutine.
- **"Tasks nest?"** Yes — `NewTask` with a ctx that already carries a task
  creates a subtask; the viewer shows the hierarchy.
- **"Do I need Chrome?"** For the timeline view, effectively yes (it's the
  Chromium trace viewer). The analysis pages and profiles work anywhere.
- **"Why is `channel send` instant?"** The channel has a buffer of 5 and the
  receiver keeps up. Fun live experiment if there's time: change
  `make(chan string, 5)` to `make(chan string)` and re-trace — sends still
  barely block because the receiver is always parked and ready. That
  intuition gets thoroughly broken in exercise 3.
- **"How do I get a trace from my service in prod?"** `net/http/pprof`'s
  `/debug/pprof/trace?seconds=N` endpoint, or the flight recorder for
  after-the-fact capture.

## Presenter Notes
- If the room's wifi/browser situation is bad, `go tool trace -http=:8080
  channel.trace` and share the URL, or fall back to the headless profiles
  (`-pprof=`) to keep moving.
- `tasks.diff` is verified against this exact `channels.go`; if you edit the
  demo, regenerate the diff or `git apply --check` before going on stage.
- Timing sanity check: the traced program takes ~150ms (5 × ~20ms requests +
  overhead); if it takes multiple seconds, something is off with the local
  server.
