# Presenter Run-of-Show

Everything here is for the presenter; students never need this file. Each
section README is the teaching script, this page is the map of the whole
4 hours, the timing pressure valves, and the day-before checklist.

To project the material rather than read from the READMEs, run `./slides.sh`,
there is a `.slide` deck per section with the README prose in its speaker
notes (press `N`). See [SLIDES.md](SLIDES.md).

## The Arc

Three tools, three lenses on the same theme, *what did the program actually
do?*:

1. **Race detector** (Part I): ground truth about memory access ordering.
2. **Execution tracer** (Part II): ground truth about scheduling and time.
3. **Delve** (Part III): ground truth about program state, on demand.

The 2026 framing bookends the day: open with "AI writes more of the code, so
you debug more code you didn't write, these tools produce the evidence,"
and close with the two AI beats that cash it in: the trace-analyzer MCP
server (Part II capstone) and an agent driving Delve live (Part III finale).

## Run of Show (4:00 total)

The three section cores sum to exactly 240 minutes, so intro, breaks, and
the AI finale must be bought from the pressure valves below. This is the
recommended cut:

| Clock | Block | Notes |
|-------|-------|-------|
| 0:00–0:10 | Welcome, AI-era framing, `./setup.sh` check | Framing: 3 sentences, not a lecture, the finale proves it |
| 0:10–1:08 | **Part I: Race Detector** (58 min) | Per its README schedule; ex4-testing is homework by default |
| 1:08–1:18 | Break | |
| 1:18–2:33 | **Part II: Execution Tracer** (75 min) | Compress ex2-flightrecorder from exercise to guided walkthrough (–15); ex4-traceagent's *Stage 2 demo* happens in the finale block |
| 2:33–2:41 | Break | |
| 2:41–3:41 | **Part III: Delve** (60 min core) | Run demo + ex1 + ex2 per its README; ex3-inventory → take-home (its README already flags this) |
| 3:41–3:58 | **AI finale** (17 min) | Beat 1: `analyze_trace` via the ex4-traceagent MCP server (5 min, pre-wired). Beat 2: agent drives Delve on ex2-dispatcher via mcp-dap-server (12 min, script in ex4-agent README) |
| 3:58–4:00 | Wrap: what to run in CI tomorrow | `-race` in CI, flight recorder in prod, `dlv attach` in your pocket |

### Pressure valves (in the order to pull them)

1. **ex3-inventory → take-home** (–15 min): planned above; the watchpoint
   walkthrough reads well solo.
2. **ex2-flightrecorder → guided walkthrough** (–15 min): planned above;
   you drive, students watch, they rerun it at home.
3. **Skip finale Beat 1** (–5 min): the DAP agent demo carries the message
   alone if needed.
4. **ex3-io → take-home** (–20 min): last resort; it's Part II's best
   exercise, cut it only if the morning ran long.

Running *ahead*? Restore valves in reverse order, or run ex4-testing live
in Part I (15–20 min, fully scripted).

### Bonus material map (never scheduled, always available)

| Exercise | What it is | When to use |
|----------|-----------|-------------|
| [01/ex4-testing](01-race-detector/exercises/ex4-testing/) | `go test -race` + `testing/synctest` lab | Homework, or live if ahead |
| [02/ex4-traceagent](02-execution-tracer/exercises/ex4-traceagent/) Stage 1 | Programmatic trace parsing (students code) | Homework; only its Stage 2 demo is in the finale |
| [03/ex1 leak-profile stretch](03-delve/exercises/ex1-fanout-fanin/) | the `goroutineleak` profile (experiment on 1.26, GA on 1.27) | 5-min aside during ex1 if the room is fast |

## Day-Before Checklist

- [ ] `./setup.sh` passes on the presenting machine (Go 1.26+, Delve ≥ 1.27.0).
- [ ] `dlv version` reports **1.27.0 or newer**. Delve enforces a Go range and
      hard-errors outside it: 1.26.0–1.26.3 cover Go 1.24–1.26, and only
      1.27.0 covers Go 1.27. `setup.sh` installs Delve only when it is
      *missing*, so a stale existing install passes setup and fails in Part III.
- [ ] `go version` reports a **released** 1.26.x, not a release candidate.
      `setup.sh`'s check does not catch this: `sort -V` orders `1.26` before
      `1.26rc1`, so any `1.26rcN` satisfies the `1.26+` gate.
- [ ] Check whether **Go 1.27 has shipped** (expected August 2026, i.e. possibly
      the week of the conference). Both Part I and Part II end on a "Coming in
      Go 1.27" slide; if it's out, say "shipped" instead of "coming", and drop
      the `GOEXPERIMENT=goroutineleakprofile` step from the Part III ex1 stretch,
      the flag is deleted in 1.27.
- [ ] `./scripts/check_slides.sh` passes, then `./slides.sh` and press `N` once
      to confirm the notes window opens (pop-ups must be allowed for
      `127.0.0.1:3999`).
- [ ] `./scripts/exercise_checker.sh`, expect the four Part I ❌ rows
      (exercises ship unsolved) and ✅ everywhere else.
- [ ] Regenerate presenter traces: `*.trace` is gitignored, so a fresh clone
      has none. Run the Part II demo and each Part II exercise program once;
      keep the trace files where their READMEs expect them.
- [ ] `go install github.com/go-delve/mcp-dap-server@latest` and re-run the
      smoke test in [03/ex4-agent](03-delve/exercises/ex4-agent/README.md),
      the project is pre-release with no tagged versions, so verify against
      whatever @latest is *that week*.
- [ ] Wire both MCP servers into Claude Code (`claude mcp add` commands in
      the ex4-traceagent and ex4-agent READMEs) and dry-run both finale
      beats end-to-end.
- [ ] Pre-fetch Part II capstone deps: `go mod download` in
      `02-execution-tracer/exercises/ex4-traceagent/` (the only network
      dependency in the repo).
- [ ] Build the leak-profile binary once:
      `GOEXPERIMENT=goroutineleakprofile go build` in ex1-fanout-fanin
      (commands in its README stretch section).
- [ ] `./cleanup.sh && git status`, tree clean except your regenerated traces.

## Live-Demo Safety Nets

- **Every exercise is deterministic**: the Part III deadlocks wedge the same
  way every run (ex2-dispatcher always dies dispatching job 10); Part I
  races fire under `-race` on every run. If something behaves oddly, you've
  likely got a stale edit, `git checkout -- <dir>` and rerun.
- **Demo diffs**: Part I's demo applies `mutex.diff → trace.diff →
  fix-mutex.diff → atomic.diff` *in sequence from the repo root*; reset with
  `git checkout -- 01-race-detector/demo/counter.go`. Part II's demo uses
  `tasks.diff` the same way.
- **Agent demos**: failure modes and live recovery steps are scripted in the
  ex4-agent README (server needs `dlv` on PATH; tools appear only after a
  session starts; evaluate needs a frame at the fatal-error stop). If the
  agent flails, the fallback is the same investigation by hand in `dlv`,
  which is itself the discussion point.
- **Section wrap-ups**: Part I's README schedules a 2-minute wrap ("when to
  run `-race`, what it can't find"); for Parts II and III, close on the same
  shape, *when to reach for this tool, and what it can't tell you*, before
  the break.
