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

Stage 1 first build downloads two modules (`golang.org/x/exp`, the MCP
SDK) — one-time network required.

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
     transitions chops a single 563ms sched wait into per-generation
     ~10ms segments. (This mistake bites.)

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
    CLI's to the microsecond. That's the trust-building moment.

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
analysis, compare the per-group numbers. They match to the microsecond.

For Stage 2 verification: the MCP `tools/list` response includes both
`analyze_trace` and `top_blocking`, with descriptions that pin agent
behavior. `claude mcp list` shows `gotrace: ✓ connected`.

## Ask the room
- What's the difference between *Runnable* and *Running* time in this
  output, and why does keeping them separate matter for latency work?
- The analyzer emits ~a few hundred bytes of JSON. The raw trace is
  megabytes. What did you *lose* in the compression, and when would that
  loss bite?
- If you were adding a fourth tool for your agent, what would it be —
  and what would you compute? (Suggest a `timeline(path, goroutine)` or
  `regions(path)` tool.)
- Where in your own service would a flight-recorder-plus-agent workflow
  pay off *today*? What alert would trip the snapshot?

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
- **First build downloads modules.** Two modules on first invocation
  (`golang.org/x/exp`, the MCP SDK). If the room has no network, do this
  in setup or bring a pre-warmed module cache.
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
