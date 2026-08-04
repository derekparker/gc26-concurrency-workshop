# Demo: A First Trace, Then Tasks & Regions

A deliberately tiny program: a `sender` goroutine fetches five messages from
an HTTP API (a local `httptest` server with ~20ms latency, no conference
wifi required) and pushes them down a buffered channel; a `receiver`
consumes them. No bug to find here. The goal is to get comfortable
**opening, navigating, and reading a trace**, then to see how
`runtime/trace` annotations (tasks, regions, logs) turn an anonymous
timeline into *your* program's story.

## Part 1: Capture and Tour a Raw Trace

```bash
cd 02-execution-tracer/demo
go run channels.go
go tool trace channel.trace
```

(`go run channels.go` writes `channel.trace` itself, so the first command
above produces the file the second one opens. `*.trace` is gitignored, so a
fresh clone has none, there is no checked-in backup, running the program is
what creates it.)

Read through `channels.go` first, it's ~70 lines. Notice `trace.Start(f)` /
`defer trace.Stop()`: that's the entire integration cost. (The other
capture methods, `go test -trace`, `/debug/pprof/trace`, flight recorder,
come in exercise 2.)

`go tool trace` opens a landing page in the browser. Tour it top to bottom:

### 1. "View trace by proc", the timeline
- **What you should see:** one row per logical processor (PROCS), mostly
  *empty*. Five thin bursts of activity spaced ~20ms apart, one per HTTP
  request, with `net/http` internals and our goroutines flickering in and
  out. Above the procs: the Goroutines, Heap, and Threads counter rows.
- The keyboard is the whole game: `w`/`s` zoom (zoom into one of the five
  bursts), `a`/`d` pan, click a slice to see its stack trace and duration
  in the bottom panel.
- Click a `main.sender` slice: the bottom panel shows the call stack. The
  program ran for ~105ms, so why is almost every pixel idle? It's waiting
  on the network, and *waiting leaves almost no trace on a CPU timeline*.
  The tracer, unlike a CPU profile, knows exactly what it was waiting on.

### 2. "Goroutine analysis" (`/goroutines`)
- Table of goroutines grouped by start location: `main.sender`,
  `main.receiver`, `main.main`, plus `net/http` internals.
- Click `main.sender`. **What you should see:** a breakdown row where
  Execution time is tiny (hundreds of µs to ~1ms) and the dominant column is
  **Block time (select)**, ~105ms. Note what it is *not*: `Block time
  (syscall)` reads `0s`, and there's no network column here at all.
  `http.Transport.roundTrip` parks the caller in a `select` while a
  *different* goroutine does the I/O. Scroll down to
  `net/http.(*persistConn).readLoop` and you'll find that same ~105ms as
  **Block time (network)**. "My" goroutine's wait and the kernel-level wait
  are accounted on two different goroutines, exactly the kind of thing you
  cannot infer from reading the source. The per-group profile links are
  here too (Network wait, Sync block, Syscall, Scheduler wait).
- Click `main.receiver`: dominated by "Block time (chan receive)", it spent
  its life parked on `<-ch`. **The tracer accounts for time spent doing
  nothing**, which is precisely what profilers can't do.
- Now click the **graph** link on each of the four per-group profiles on the
  `main.sender` page. Two render a real pprof call graph; two render an empty
  canvas ("Showing nodes accounting for 0, 0% of 0 total"). This maps
  directly onto the breakdown row above:
  - **Sync block**: has data, ~105ms of `runtime.selectgo` under
    `net/http.(*persistConn).roundTrip`. It's sender's own call stack, the
    tracer buckets "parked in a select" as a synchronization block, which is
    exactly the Block time (select) number from the table.
  - **Scheduler wait**: has data too, though the total is tiny (tens of µs,
    matching the small Sched wait time column). The graph is *not* sender's
    own code, the nodes are named `net/http.(*persistConn).readLoop` and
    `.writeLoop`. Scheduler wait measures the gap between "made runnable" and
    "actually running on a P," so the stack it records is captured at the
    moment something else woke sender up, here, a channel send inside
    `readLoop`/`writeLoop` handing the response back. That's the tracer
    naming who unparked you, and it's the breadcrumb to the next goroutine
    group.
  - **Network wait / Syscall**: both empty. Sender never touches a raw fd or
    issues a syscall itself, `net/http` hands the socket off to the
    connection's own reader/writer goroutines, so those events live entirely
    in `readLoop`'s and `writeLoop`'s own per-group profiles, not sender's.
  In general: a goroutine's per-group profiles only have data for the
  categories of runtime event *that goroutine itself* generated during the
  trace, empty graphs aren't a bug, they're telling you "this goroutine never
  did that kind of thing."
- This also answers "how do I find `net/http.(*persistConn).readLoop` without
  already knowing it exists?", the walkthrough above got there by manually
  scanning the goroutine table, but you don't have to: click **graph** on
  `main.sender`'s **Scheduler wait** profile, and `readLoop`/`writeLoop` are
  named right there as the goroutines whose channel sends unparked it. From
  there, go back to `/goroutines` and click that start location to see its
  own breakdown, where the ~105ms **Block time (network)** figure lives.
- Follow that breadcrumb and land on `readLoop`'s own page. Its four
  per-group graphs tell the other half of the story, and the relationship is
  symmetric in a satisfying way:
  - **Scheduler wait**: has data, but now it's tiny (single-digit µs), and
    the named node is `main.sender` (via `io.ReadAll` →
    `net/http.(*bodyEOFSignal).Read/.condfn` → `readLoop.func4` →
    `runtime.chansend1`). Same mechanism as before, just pointed the other
    way: once sender finishes reading the response body, that completion
    signals `readLoop` over an internal channel so it can go around for the
    next read, and *that's* what briefly wakes `readLoop` up. Sender and
    readLoop are woken by each other, and each one's Scheduler-wait graph
    names the other as proof.
  - **Network wait**: this is the big one, ~106ms, essentially the whole
    trace. The call graph is Go's actual networking stack laid out top to
    bottom: `readLoop` → `bufio.(*Reader).Peek/.fill` →
    `net/http.(*persistConn).Read` → `net.(*conn).Read` →
    `net.(*netFD).Read` → `internal/poll.(*FD).Read`. That bottom frame is
    where the goroutine is parked, not executing, waiting on the runtime's
    network poller (epoll/kqueue) for the socket to become readable. This is
    the ~105ms our five ~20ms requests actually cost, now attributed to the
    goroutine that's really waiting on the kernel, matching the Block time
    (network) column on `readLoop`'s breakdown row.
  - **Syscall**: also has data, but only ~29µs, a hundredth of a percent of
    the network-wait figure, same call stack, bottoming out in
    `syscall.Read`/`syscall.syscall` instead of `internal/poll.(*FD).Read`.
    This is the distinction worth making explicit, since "blocked in the
    syscall reading from the fd" is a common but slightly wrong mental
    model: Go's netpoller does non-blocking reads. `readLoop` isn't sitting
    inside a blocking `read()` for 105ms; it's parked (Network wait) while
    epoll/kqueue waits for data, then briefly resumes to actually execute
    `read()` (Syscall execution time) once the poller says data is ready.
    The syscall itself is cheap, the wait for it to be worth calling is not.
  - **Sync block**: small (tens of µs) `runtime.selectgo` directly under
    `readLoop`, unrelated to sender, this is `readLoop`'s own internal
    `select` for shutting down or handing an idle connection back to the
    pool between reads.

### 3. The derived profiles (skim these, you'll use them for real in the exercises)
- From the landing page: Network blocking / Synchronization blocking /
  Syscall / Scheduler latency profiles, pprof graphs computed from the
  trace. The exercises use them; here just note that they exist. One
  sentence on each, the deep dive comes in later exercises:
  - **Network blocking profile**: where goroutines across the whole program
    are parked waiting on network I/O, an fd not being ready yet. This is
    the aggregate, whole-trace version of the `readLoop` Network wait graph
    just walked through above.
  - **Synchronization blocking profile**: where goroutines are parked on
    channels, mutexes, or `select`, everything seen on `main.sender`
    (select) and `main.receiver` (chan receive).
  - **Syscall profile**: time actually spent inside a blocking syscall once
    it's running, not waiting for one to be worth calling. Usually small;
    it's the needle, network/sync blocking is the haystack.
  - **Scheduler latency profile**: time spent runnable but not running,
    queued for an available P. This is contention for CPU itself, distinct
    from all three profiles above, which are about *why* a goroutine isn't
    runnable at all. It reads differently from the per-goroutine graphs
    above, so it's worth slowing down for:
    - Same rule as the per-goroutine Scheduler wait graphs from the
      goroutine-analysis walkthrough, just merged across every goroutine in
      the trace instead of scoped to one: the nodes are wakeup call sites
      (whoever ran the `chansend`/`select`/broadcast that made some other
      parked goroutine runnable again), not the blocked goroutine's own
      code. Expect to see the same cast of characters as before,
      `runtime.chansend1` and `runtime.selectgo` dominate, plus some new
      ones like `internal/poll.setDeadlineImpl` (setting a read deadline
      can itself flip a parked reader to ready) and `sync.(*Cond).Broadcast`
      (net/http's background connection reader).
    - The number that matters most is the total at the top, "Showing nodes
      accounting for X, Y% of Z total." In this demo it's minuscule, well
      under a millisecond out of a 100ms+ trace, because there are only a
      couple of live goroutines and plenty of Ps to go around. A small
      total here means the runtime almost never made anyone wait for a
      core. A *large* one, comparable to the trace duration, is the
      signature of CPU oversubscription: more runnable goroutines than
      GOMAXPROCS has room for. That's a fundamentally different problem
      from network or lock contention, and this is the one profile that
      can actually show it to you.
    - Because it's whole-trace, this page can't tell you *whose* wait it is.
      For that you'd go back to `/goroutines` and use the per-group
      Scheduler wait link, exactly what you used earlier to find `readLoop`
      from `main.sender`.

## Part 2: Tasks, Regions, and Logs

Motivation: in the raw trace you had to *recognize* goroutines by function
name and guess what "iteration 3" was. The `runtime/trace` annotation API
lets the program label its own work.

Apply the prepared diff (from the **repo root**):

```bash
git apply 02-execution-tracer/demo/tasks.diff
```

Look at the diff (`git diff`) and note the three APIs:
- `ctx, task := trace.NewTask(ctx, "requestAndSend")` + `defer task.End()`,
  one logical *unit of work*, potentially spanning many goroutines. The ctx
  carries the task; everything created from it is grouped under it.
- `trace.WithRegion(ctx, "http request", func() {...})`, brackets a code
  interval *within one goroutine* (regions must begin and end in the same
  goroutine; tasks are the cross-goroutine construct).
- `trace.Log(ctx, "sending request", "3")`, a timestamped key/value pinned
  to the task.

Re-run and re-open:

```bash
cd 02-execution-tracer/demo
go run channels.go
go tool trace channel.trace
```

### "User-defined tasks" (`/usertasks`)
- **What you should see:** a table, Task type `requestAndSend`, Count 1,
  and a duration histogram — the task lands in the **100ms** bucket
  (`100ms`–`158ms`), elapsed ~106ms.
- Click the count/bucket link to open the task list, then look at the
  single task's event log: a chronological table (When | Elapsed |
  Goroutine | Events) interleaving *both goroutines*: `task
  "requestAndSend" begin`, `region "receive loop" begin` (G of receiver),
  `log "0"`, `region "http request" begin/end` (~20ms apart), `region
  "channel send" begin/end` (microseconds, the buffer absorbs it),
  repeating five times.
- This table alone answers "what did request 3 do, and when?", per-request
  latency accounting with ~10 lines of annotation.
- Click **(goroutine view)** on the task: the timeline again, but filtered
  to the task, with region bars drawn under the goroutines.

### "User-defined regions" (`/userregions`)
- Region types with counts and duration histograms: `http request` ×5
  (~20ms each), `channel send` ×5 (~µs), `receive loop` ×1 (~whole run).
  Note the unnamed `""` rows in the list too. Those are *not* the standard
  library annotating itself, no non-test stdlib package calls
  `trace.StartRegion` at all. They're synthesized: a goroutine created while
  its parent is inside an active region inherits an implicit whole-lifetime
  region under the same task, which is how the transport's dial goroutines
  end up here. Interesting if you're curious, but not essential to what
  follows.

Reset for another pass through the demo:

```bash
git apply -R 02-execution-tracer/demo/tasks.diff
```

## Questions worth sitting with
- **What does this cost in production?** ~1–2% CPU since Go 1.21. The
  bigger cost is trace *volume* (MB/s on busy services), which is what the
  flight recorder solves (exercise 2).
- **Can a region span goroutines?** No, regions are per-goroutine by
  design. Use a task and pass its ctx; start a region in each goroutine.
- **Do tasks nest?** Yes, `NewTask` with a ctx that already carries a task
  creates a subtask; the viewer shows the hierarchy.
- **Do I need Chrome?** For the timeline view, effectively yes (it's the
  Chromium trace viewer). The analysis pages and profiles work anywhere.
- **Why is `channel send` instant?** The channel has a buffer of 5 and the
  receiver keeps up. Try changing `make(chan string, 5)` to `make(chan
  string)` and re-trace, sends still barely block because the receiver is
  always parked and ready. That intuition gets thoroughly broken in
  exercise 3.
- **How do I get a trace from my service in prod?** `net/http/pprof`'s
  `/debug/pprof/trace?seconds=N` endpoint, or the flight recorder for
  after-the-fact capture.

## Troubleshooting
- If your browser or network situation is awkward, `go tool trace
  -http=:8080 channel.trace` and open that URL yourself, or fall back to
  the headless profiles (`-pprof=`).
- `tasks.diff` is verified against this exact `channels.go`; if you edit
  the demo, regenerate the diff or run `git apply --check` first.
- Sanity check on timing: the traced program takes ~105ms (5 × ~20ms
  requests + a few ms of overhead); if it takes multiple seconds, something
  is off with the local server.
