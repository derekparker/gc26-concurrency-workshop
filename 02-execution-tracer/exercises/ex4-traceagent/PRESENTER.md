# Presenter: Build a Trace Analyzer Your AI Agent Can Use

> Presenter-only. Students use README.md.

## Goal
Two goals, one exercise.

- **Stage 1:** teach that `go tool trace` is *one* consumer of the trace
  format, not the only one. With `golang.org/x/exp/trace` — the same
  decoder — anything you learned to spot by eye in ex1 becomes ~40 lines
  of state-machine code that emits a compact diagnosis.
- **Stage 2:** hand that capability to an AI agent over MCP. This is the
  workshop's **AI-era thesis made concrete**: an agent can't attach a
  debugger to your incident and shouldn't guess from source. A trace is
  ground truth — but a raw multi-MB binary doesn't fit in a context
  window. *You* decide what the trace is distilled into; the agent
  reasons from that.

Stage 2 is often the workshop finale. If you're doing the finale, plan
to demo the Claude Code + MCP integration live.

## Reproduce
Stage 1 needs a **broken** ex1 trace. Traces are gitignored — everyone
generates their own:

```bash
cd 02-execution-tracer/exercises/ex1-scheduling
# If you already fixed ex1, revert first — the whole point is re-finding
# that bug programmatically.
go run main.go                   # repeat until p99 >300ms
cp scheduling.trace broken.trace
cd ../ex4-traceagent
```

If the analyzer chokes with `expected batch event, got event 0` (or
similar), the trace is bad, not the code — ~1 run in 25 produces one.
Regenerate; see ex1's pitfalls.

Stage 1 first build downloads 8 modules, ~49 MB (`golang.org/x/exp`, the
MCP SDK and its dependencies) — one-time network required.

Run the un-completed analyzer to see the bookkeeping runs but nothing is
accounted for:

```bash
go run ./cmd/analyze ../ex1-scheduling/broken.trace
```

Expected: goroutine groups with correct counts and **all-zero durations**.

After the three TODOs, expected (numbers from an actual bad run —
p99 525ms, max 587ms):

```text
trace duration: 3s | goroutines: 1010 | groups: 11

GOROUTINE GROUPS (by start function, top 10 by total time)
START FUNC                                           COUNT  RUNNING  RUNNABLE  BLOCKED  SYSCALL  MAX SCHED WAIT
sync.(*WaitGroup).Go.func1                           1000   23.92s   31m37.5s  0        0        2.97s
main.main                                            1      989µs    4µs       2.99s    9µs      3µs
main.heartbeat                                       1      403µs    1.07s     1.92s    0        562.93ms
...

TOP BLOCKING SITES (by total blocked time, top 10)
SITE                                       REASON        COUNT  TOTAL  MAX
runtime.chanrecv1 (chan.go:509)            chan receive  29     4.99s  1s
sync.(*WaitGroup).Wait (waitgroup.go:206)  sync          1      2.99s  2.99s
main.heartbeat (main.go:84)                select        101    1.92s  35.33ms

LONGEST SCHEDULER WAITS (single runnable→running gaps, top 10)
WAIT   AT      GOROUTINE  START FUNC
2.97s  +869µs  G1004      sync.(*WaitGroup).Go.func1
2.97s  +871µs  G1007      sync.(*WaitGroup).Go.func1
```

That's the ex1 diagnosis, computed. The room should feel the "click".

## Root cause
There isn't a bug *in this exercise's code*; the "bug" is the one from
ex1 — scheduler starvation caused by unbounded fan-out — and the exercise
asks whether you can compute the ex1 diagnosis from the same trace file,
without opening the viewer.

The un-completed analyzer emits zeroes because the three record-\* hooks
are empty. Without them, the state machine advances `g.state` but never
credits time to buckets, never captures block sites, and never records
runnable→running gaps. Real diagnostic value lives in the accounting, not
the state machine.

## Walkthrough
This has two stages. Do Stage 1 on time and Stage 2 as time and
appetite allow. Stage 2 is the finale-friendly one.

### Stage 1: Programmatic Trace Analysis

1. **Frame the problem (~1 min).** In ex1 the room diagnosed scheduler
   starvation *by eye*. Ask: could we compute this? Answer: yes, the
   trace viewer is one consumer of a well-specified format.
   `golang.org/x/exp/trace.NewReader` is the same decoder the viewer
   uses.

2. **Show the layout.** Only one file has TODOs:
   ```
   analyzer/analyzer.go   the event loop (provided) + 3 TODO functions (yours)
   analyzer/render.go     output types + rendering (provided)
   cmd/analyze/           the CLI (provided)
   cmd/mcp/               the MCP server for Stage 2 (provided)
   ```

3. **Read the skeleton.** In `analyzer/analyzer.go`, walk through
   `Analyze` — the ~40-line event loop:
   - `trace.NewReader` decodes the binary trace.
   - Filter for `EventStateTransition` events on `ResourceGoroutine`.
   - Ordering matters: call `recordSchedWait` and `recordBlocking`
     **before** `recordStateTime` advances `g.since`.
   - `from == to` transitions are skipped — the tracer re-asserts every
     G's state at generation boundaries (~1/sec). Treating those as real
     transitions chops a long wait at those boundaries: the worst sched
     wait reads as 1.0s instead of 3.3s, the heartbeat as 760ms instead
     of 2.2s. Still bad-looking, but off by 3–4×. (This mistake bites.)

4. **Baseline run.**
   ```bash
   go run ./cmd/analyze ../ex1-scheduling/broken.trace
   ```
   Point at the all-zero columns. State: *"the state machine works;
   nobody's doing the arithmetic."*

5. **Fill in TODO 1 — `recordStateTime`.** ~4 lines:

   ```go
   func (a *analysis) recordStateTime(g *gInfo, from trace.GoState, ts trace.Time) {
       if b := bucket(from); b >= 0 {
           g.buckets[b] += ts.Sub(g.since)
       }
       g.since = ts
   }
   ```

   Say aloud: *"This one function reproduces the trace viewer's
   Execution / Block / Sched-wait columns."*

6. **Re-run.**
   ```bash
   go run ./cmd/analyze ../ex1-scheduling/broken.trace
   ```
   The GOROUTINE GROUPS table now has real numbers. Point at the
   `sync.(*WaitGroup).Go.func1` row: Count 1000, tens of minutes of
   cumulative RUNNABLE against seconds of RUNNING. That's ex1's bug,
   quantified.

7. **Fill in TODO 2 — `recordBlocking`.** ~18 lines. Two edges:

   ```go
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
   ```

   Emphasize the invariant: on the way *into* `GoWaiting`, capture where
   we're blocking; on the way *out*, credit the elapsed time to that
   site. Ends on the edge OUT of `GoWaiting`, not on the next Running —
   the gap between wake and running is *scheduler wait*, not blocking.
   Keeping those two separate is why ex1's bug was invisible in CPU
   profiles.

8. **Re-run — TOP BLOCKING SITES populates.**
   ```bash
   go run ./cmd/analyze ../ex1-scheduling/broken.trace
   ```
   Point at `main.heartbeat (main.go:84) select ... 1.92s` — legitimate
   waits for `<-ticker.C`. Contrast with the next TODO's output.

9. **Fill in TODO 3 — `recordSchedWait`.** ~8 lines:

   ```go
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

   `minSchedWait = 1 * time.Millisecond` (see the const at top of
   `analyzer.go`) — waits shorter than 1ms still count toward the
   Runnable bucket but aren't reported as individual events.

10. **Final run — all three sections populated.**
    ```bash
    go run ./cmd/analyze ../ex1-scheduling/broken.trace
    ```
    Walk the room through the output:
    - `sync.(*WaitGroup).Go.func1` × **1000**, ~24s running vs.
      ~31 minutes cumulative RUNNABLE. Point at the ratio.
    - `main.heartbeat`: 403µs running, **1.07s RUNNABLE**, **max sched
      wait 562.93ms** — for a goroutine with a 10ms deadline.
    - LONGEST SCHEDULER WAITS: individual gaps of ~3s for the last few
      Gs to ever get scheduled. Say aloud: *"That is the number ops
      would have paged you for."*

11. **Sanity-check against the viewer.** Say aloud: *"Same decoder, same
    events."* If time and screen space allow, tile
    `go tool trace ../ex1-scheduling/broken.trace` next to your CLI
    output. The Goroutine analysis page's per-group numbers match the
    CLI's to within microseconds. That's the trust-building moment.

12. **Show the JSON output.**
    ```bash
    go run ./cmd/analyze -json ../ex1-scheduling/broken.trace | head -40
    ```
    Point at `Duration.MarshalJSON` producing `"1.31s"` strings. This is
    what feeds the MCP tool: agent-friendly, human-readable.

13. **`-top N` knob.**
    ```bash
    go run ./cmd/analyze -top 3 ../ex1-scheduling/broken.trace
    ```
    For screens without much vertical space.

### Stage 2: Hand It to an Agent (workshop finale)

> **Prerequisite, do this before you go on stage.** The two MCP tools are
> thin wrappers over `analyzer.AnalyzeFile`, so they inherit whatever state
> the TODOs are in. Against the repo as shipped, `top_blocking` returns
> `top 10 blocking sites (of 0)` and `analyze_trace` reports all-zero
> durations, the agent gets an empty report and the finale dies. This bites
> specifically when Stage 1 was assigned as homework rather than run in the
> room, which is the default in the run-of-show.
>
> ```bash
> cd 02-execution-tracer/exercises/ex4-traceagent
> git apply solution.diff        # the three TODOs, exactly as in README.md
> go run ./cmd/analyze ../ex1-scheduling/broken.trace   # sanity check: real numbers
> ```
>
> Restore the TODOs afterwards with `git apply -R solution.diff` so the
> exercise ships unsolved.

14. **Frame the payoff.** Everything you just built takes a trace file and
    produces ~a few hundred bytes of structured diagnosis. That fits in
    an LLM context window. An agent that can call this can reason from
    ground truth instead of guessing.

15. **Read `cmd/mcp/main.go` (~1 min).** ~100 lines, mostly plumbing:
    - Tool `analyze_trace(path, format)`: full summary.
    - Tool `top_blocking(path, n)`: shorter, focused list.
    - Both are thin wrappers over `analyzer.AnalyzeFile`.
    - `mcp.NewServer` + `mcp.AddTool` + `server.Run(ctx, &StdioTransport{})`
      is the whole integration.
    - Tool descriptions are important — the agent reads them to decide
      which to call. Point at the phrases in the description:
      *"High 'runnable' time means scheduler starvation; high 'blocked'
      time means contention or serialization."* You're teaching the
      agent what its own output means.

16. **Drive it by hand first** (works on any machine, no Claude Code):

    ```bash
    {
      echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"me","version":"0"}}}'
      echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
      echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
      sleep 2
    } | go run ./cmd/mcp
    ```

    Point at the tool schemas in the response. Say: *"This is JSON-RPC
    over stdio. Any MCP client can drive this."*

17. **Wire it into Claude Code** (if you have it installed and the room
    can watch):

    ```bash
    # from this directory:
    claude mcp add gotrace -- go -C "$(pwd)" run ./cmd/mcp
    claude mcp list                       # expect: gotrace: ✓ connected
    claude
    ```

    Paste (adjust the absolute path):

    > Here's an execution trace from a Go service with terrible tail
    > latency: `<absolute path>/02-execution-tracer/exercises/ex1-scheduling/broken.trace`.
    > Use the gotrace tools to diagnose it. What is wrong, and what
    > would you change in the code?

    Narrate what the agent is doing as it does it: calling
    `analyze_trace`, seeing the 1000-goroutine group with minutes of
    runnable time against a starved single G, naming *scheduler
    starvation*, proposing a worker pool sized to `GOMAXPROCS`. The point:
    it names the same bug the room named in ex1 — because it's reading
    the same evidence, in a form it can consume.

    Clean up when you're done:
    ```bash
    claude mcp remove gotrace
    ```

18. **Point at flight recorder tie-in.** The same analyzer eats ex2's
    snapshot verbatim — no code changes:

    ```bash
    go run ./cmd/analyze ../ex2-flightrecorder/flightrecorder.trace
    ```

    > Prerequisite: `flightrecorder.trace` is gitignored, so it only
    > exists if ex2 has actually been run with both TODOs in place. On a
    > fresh clone this is `no such file`. Run ex2 first, or skip the beat.

    On an ex2 snapshot, `top_blocking` surfaces the convoy directly:

    ```text
    sync.(*RWMutex).RLock (rwmutex.go:74)   sync   8   2.11s   264.8ms
    ```

    Eight workers, ~265ms each, on the refresher's write lock —
    quantified. Say aloud: *"An agent with these two tools plus a flight
    recorder snapshot from an incident ticket can do the ex2
    investigation end to end, without access to the running system."*

19. **Land the thesis.** *"'I saw it in the viewer' becomes 'we compute
    it in CI', 'the incident bot attaches it to the ticket', 'the agent
    diagnosed the snapshot' — all the same ~40 lines of state machine
    you wrote today. Diagnostic artifacts beat vibes, for humans and for
    agents alike."*

## Fix
Solutions to all three TODOs, exactly as in the README:

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

Verify:

```bash
go run ./cmd/analyze ../ex1-scheduling/broken.trace
```

Expected: three populated sections that recognizably reproduce the ex1
diagnosis (1000-goroutine group with minutes of RUNNABLE, `main.heartbeat`
with hundreds of ms max sched wait, individual gaps in the seconds range
for the last-scheduled Gs).

Cross-check: **Same decoder, same events.** Open
`go tool trace ../ex1-scheduling/broken.trace`, click Goroutine
analysis, compare the per-group numbers. They match to within microseconds.

For Stage 2 verification: the MCP `tools/list` response includes both
`analyze_trace` and `top_blocking`, with descriptions that pin agent
behavior. `claude mcp list` shows `gotrace: ✓ connected`.

## Ask the room
Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- What's the difference between *Runnable* and *Running* time in this
  output, and why does keeping them separate matter for latency work?

  *Running* is `bRunning` in `analyzer.go` — wall-clock time the goroutine
  was actually executing on a P. *Runnable* is `bRunnable` — time it was
  ready to go but had no P/OS thread to run on, i.e. pure scheduler queueing
  delay. `bucket()` (analyzer.go:150) maps `trace.GoRunning` and
  `trace.GoRunnable` to two separate buckets precisely so they never get
  summed into one "wall time" number. A slow request can be slow for two
  completely different reasons — it's doing real work (high Running) or
  it's sitting in line for a CPU (high Runnable, from `GOMAXPROCS`
  contention or too many runnable goroutines) — and those have opposite
  fixes: optimize the code, versus reduce concurrency or add capacity. The
  `main.heartbeat` row in the demo output is the whole argument in one line:
  403µs Running against 1.07s Runnable. If those had been collapsed into one
  column you'd conclude the heartbeat handler itself was slow and go
  profiling the wrong code. Keeping them apart is also exactly why ex1's bug
  was invisible in a CPU profile — a CPU profile only samples *Running*
  time, and this goroutine was almost never running.

- The analyzer emits ~a few hundred bytes of JSON. The raw trace is
  megabytes. What did you *lose* in the compression, and when would that
  loss bite?

  Concretely, per `analyzer.go`'s `summary()`: goroutines get merged into
  groups by start function (`gr.Count++`, `gr.Running += ...`), so you keep
  aggregate durations but lose which *specific* `G` in that group of 1000
  ran long versus short. `TopBlocking` and `LongestSchedWaits` are both
  capped at `maxReported = 50` (analyzer.go:32), so a trace with more than
  50 distinct blocking sites or long waits silently drops the tail — you get
  the worst 50, not all of them. `siteOf` (analyzer.go:243) collapses a full
  call stack down to one source line. And the analyzer only looks at
  `EventStateTransition` on `ResourceGoroutine` (analyzer.go:56-63) — it
  never touches GC events, heap/memory events, network dial/read timing, or
  proc-level (`ResourceProc`) transitions that the raw trace also carries;
  those are explicitly skipped (`skip proc state transitions`). The loss
  bites whenever the bug needs per-goroutine or per-event resolution rather
  than a summary: "goroutine 4821 specifically deadlocked at 14:32:07,001"
  isn't answerable from this JSON, only "the group of goroutines started by
  X spent a lot of time blocked." If the aggregate numbers look fine but one
  goroutine out of a thousand is wedged, or the interesting behavior lives
  in a narrow time window the summary averages away, you have to go back to
  `go tool trace` on the raw file.

- If you were adding a fourth tool for your agent, what would it be —
  and what would you compute? (Suggest a `timeline(path, goroutine)` or
  `regions(path)` tool.)

  Both suggestions plug the exact gap the previous answer names.
  `timeline(path, goroutine)` would return the ordered state-transition
  sequence for one `trace.GoID` — every `from -> to` with timestamps — which
  is the per-goroutine resolution `analyze_trace` and `top_blocking`
  deliberately throw away by grouping and aggregating. The natural workflow
  is: agent calls `analyze_trace`, sees the `LONGEST SCHEDULER WAITS` table
  name `G1004`, then calls `timeline(path, 1004)` to see exactly what that
  one goroutine was doing before and after the 2.97s gap — a question the
  existing two tools structurally can't answer because they only keep
  aggregates. `regions(path)` would surface `trace.WithRegion`-annotated
  spans (user-defined logical work units, not raw scheduler states) and
  their durations — useful the moment your code marks meaningful boundaries
  like "handle one request" or "run one batch," which the scheduler's
  Running/Runnable/Blocked/Syscall buckets know nothing about. Either is a
  legitimate answer; the through-line to push students toward is that a good
  fourth tool should answer a question the first three structurally cannot,
  not just slice the same GOROUTINE GROUPS table a different way.

- Where in your own service would a flight-recorder-plus-agent workflow
  pay off *today*? What alert would trip the snapshot?

  Pose it back to the room as a discussion prompt rather than a spec to
  fill in — the useful part is them mapping it onto their own service. The
  shape of a good answer: any service with a "some requests are mysteriously
  slow, no repro, not always" symptom is a candidate — the exact profile
  ex1/ex2 are built to demonstrate, and the kind of bug that's expensive to
  reproduce on demand because it depends on load and timing you can't
  dial up locally. The trigger is whatever already pages you or is close to
  it: a p99 latency alert, an SLO burn-rate alert, a queue-depth threshold —
  something cheap to evaluate continuously. On trip, call `fr.WriteTo` (the
  same detect-then-snapshot pattern ex2's `snapshotOnce.Do` block implements
  — see ex2's PRESENTER.md) to capture the last few seconds *before* anyone's
  paged, ship that sub-megabyte file to the agent, and have `analyze_trace` /
  `top_blocking` run automatically as part of incident triage — by the time
  a human opens the ticket there's already a computed diagnosis attached,
  not just a stack of raw traces someone has to remember to go pull. The
  win over "add more logging" is that this is *ground truth* captured at the
  moment of the anomaly, not an inference from whatever you thought to log
  in advance.

## Common pitfalls
- **`from == to` transitions.** The tracer re-asserts state at
  generation boundaries (~1/sec). If a student "helpfully" removes the
  skip in `Analyze`, a single 563ms sched wait shows up as a chain of
  ~10ms gaps — the ex1 bug becomes invisible. Point at the comment.
- **Ordering.** `recordSchedWait` and `recordBlocking` read `g.since`
  *before* `recordStateTime` advances it. The skeleton's call order is
  correct; keep the logic inside the right function.
- **Blocked time ends on `GoWaiting → *`, not on the next `→ Running`.**
  A woken G is *runnable* first — that gap is scheduler latency, not
  blocking. Mixing them defeats the whole point of the exercise (and of
  the trace viewer).
- **First build downloads modules.** Eight modules, ~49 MB, on first
  invocation (`golang.org/x/exp`, the MCP SDK, and its transitive
  dependencies). At 49 MB × room size this is a real hazard: have the
  room run `go mod download` in setup, or bring a pre-warmed module cache
  on a USB stick.
- **Stage 2 without Claude Code.** The by-hand JSON-RPC demo (step 16)
  is a full substitute; don't skip it if `claude` isn't available. It's
  also how you debug any MCP server.
- **Claude Code cwd behavior.** `claude mcp add gotrace -- go -C
  "$(pwd)" run ./cmd/mcp` uses `go -C` to pin the working directory —
  necessary because the launched process might otherwise run from an
  odd cwd. Don't drop the `-C`.
- **Live-demo failure mode.** If the agent stalls or refuses, fall back
  to the by-hand JSON-RPC drive. The point of the finale is the *shape*
  (agent-consumable diagnostic artifact), not the specific vendor.
