# Presenter: An AI Agent Holds the Debugger

> Presenter-only. Students use README.md.
>
> Instructor-led, if time allows (~15–20 min). This is the workshop's
> closing act.

## Goal
Show that everything the room just learned by hand — goroutine survey,
frame-scoped evaluation, channel forensics — can be driven by a coding
agent talking MCP → DAP → Delve. The teaching point is *not* "agents
are magical"; it's "agents run the debugger, and someone still has to
know what `sendq` full of parked goroutines means." Students see the
same discipline they practiced, executed autonomously, and see exactly
where a human at the CLI is still faster.

**There is no new program in this exercise.** The debuggee is
[`ex2-dispatcher`](../ex2-dispatcher/) — its deadlock is fully
deterministic, which is what makes this demo presenter-proof.

## Reproduce

### One-time setup (do this before the session, and re-verify the
morning of)

```bash
# 1. The MCP↔DAP bridge (needs Go >= 1.26.1; toolchain auto-download handles it)
go install github.com/go-delve/mcp-dap-server@latest

# 2. dlv must be on PATH — the server spawns `dlv dap` for you
dlv version

# 3. Wire it into Claude Code (user scope so it works from any dir)
claude mcp add --scope user debugger -- $(go env GOPATH)/bin/mcp-dap-server
claude mcp list        # should show: debugger ... - ✓ Connected
```

Inside a Claude Code session, `/mcp` lists the server and its (initially
one) tool: `debug`. There are **no tagged releases yet**; `@latest` is a
pseudo-version of `main`. Re-run `go install` the week of the workshop
and dry-run the whole loop on the venue machine that morning.

### Launching the demo

Start Claude Code **in the ex2 directory** (so the agent's cwd is the
module):

```bash
cd 03-delve/exercises/ex2-dispatcher
claude
```

Paste this prompt (or read it out and let students paste it):

> This Go program (main.go in the current directory) deadlocks every
> run. Using only the debugger MCP tools, launch with the `debug` tool
> in source mode, path `<absolute path to ex2-dispatcher>`, run it
> until it stops, then determine which channel operation every user
> goroutine is stuck on and explain the deadlock cycle. Report
> goroutine-by-goroutine evidence. Do not read or modify any files.

The two constraints — *"only the debugger tools"* and *"do not read
any files"* — are the whole trick. They force the agent to reason from
the process, not pattern-match on source. Same discipline the room has
been practicing.

## Root cause
Same as ex2 — the point of this demo is watching the agent *rediscover*
it live from the debugger:

`main` runs in phases: dispatch all 12 jobs, then read all 12 reports.
With `numWorkers=3`, `cap(jobs)=4`, `cap(reports)=2`, the batch only
fits while `len(batch) ≤ 9`. Workers block sending into `reports` (2/2,
no receiver); `jobs` (4/4) then fills; `main` wedges on job 10.

Agent's final answer, on a correct run, should trace exactly that cycle
using tool results only.

## Walkthrough — scripted agent session, beat by beat

Narrate each beat to the room. Every tool call maps to a debugger move
they already know.

### Beat 1: `debug` (launch)

Tool call:

```json
{"mode": "source", "path": "<absolute path to ex2-dispatcher>", "stopOnEntry": true}
```

Response:

```
Stopped at program entry. Set breakpoints and use 'continue' to reach your code.
```

**Say out loud:** Delve compiled with `-N -l` behind the scenes — same
as `dlv debug`. Note that `debug` is the *only* tool visible before the
session starts. The other twelve appear the moment a session is live:

```
breakpoint, clear-breakpoints, continue, step, pause, restart, stop,
context, evaluate, set-variable, info, disassemble
```

### Beat 2: `continue` — the fatal throw *is* the breakpoint

No breakpoints needed:

```
Stopped: exception
Function: runtime.fatal
File: /usr/local/go/src/runtime/panic.go:1241
```

**Teaching beat:** watch whether the agent realizes this. A weak run
sets breakpoints in the worker loop first and burns turns.

### Beat 3: `info threads` — the goroutine survey

Tool call:

```json
{"type": "threads"}
```

Response:

```
Threads:
  Thread 1: [Go 1] main.main
  Thread 2: [Go 2] runtime.gopark
  ...
  Thread 35: [Go 35] main.worker
  Thread 36: [Go 36] main.worker
  Thread 37: [Go 37] main.worker
```

The IDs shift run to run (35/36/37 one run, 7/8/9 the next) — don't read a
specific number off the slide, read it off the output.

DAP "threads" are goroutines under Delve. This is the agent's version
of `goroutines -group userloc` — but without grouping. **Count the
tool calls it burns to get what one CLI command gave you.**

### Beat 4: `context` per goroutine — stacks with frame IDs

`context` with `{}` returns the current goroutine's location + full
stack + all locals. Add `{"threadId": <id from info threads>}` for another
goroutine.

Sample response (main):

```
#1 (Frame ID: 1009) runtime.chansend at /usr/local/go/src/runtime/chan.go:283
#3 (Frame ID: 1011) main.main at .../ex2-dispatcher/main.go:79
```

`runtime.chansend` under `main.main:79`, and under every `main.worker`
frame at `main.go:44`. All four user goroutines are blocked
**sending**.

### Beat 5: `evaluate` with a user `frameId` — channel forensics

**This is the demo's best teaching moment.** The first frame-less
`evaluate` will likely fail — at the exception stop the top frame is
`runtime.gopark`, so:

```json
{"expression": "jobs"}
```

returns `unable to evaluate expression`. Watch whether the agent
diagnoses the scope problem and reaches for `frameId` on its own.
That's the "does it actually understand the debugger?" moment.

Once it does:

```
{"expression": "job",      "frameId": 1011} → main.Job {ID: 10, Target: "pkg/service10"}
{"expression": "jobs",     "frameId": 1011} → chan main.Job 4/4
{"expression": "jobs.buf", "frameId": 1011} → *[4]main.Job [{ID: 8, ...},{ID: 9, ...},{ID: 6, ...},{ID: 7, ...}]
{"expression": "reports",  "frameId": 1017} → chan<- main.Report 2/2
```

Both buffers full, main wedged holding job 10, workers wedged holding
their reports. The agent now has every fact from ex2's walkthrough. A
good run writes up the cycle from tool results only.

### Beat 6: the writeup — evidence vs. vibes

Read the agent's final answer with the room. Does every claim trace to
a tool result? That transcript-as-proof is this section's closing theme.

## Fix

There is no fix to apply in this exercise — the debuggee is ex2, and
students already fixed ex2. The "fix" here is the *workflow*: an agent
that walks a frozen process reasons from evidence; an agent that can't
is pattern-matching your source. **Verify:** a headless one-shot
against the same exercise:

```bash
echo "This program deadlocks... (prompt as above)" | \
  claude -p --mcp-config mcp.json --strict-mcp-config \
  --allowedTools "mcp__debugger"
```

`mcp.json` is committed next to the exercise README and looks up
`mcp-dap-server` on `PATH`; swap in an absolute path if
`$(go env GOPATH)/bin` isn't on yours.

The captured baseline run produces the complete cycle — main parked on
`jobs` (4/4) holding job 10, all three workers parked on `reports`
(2/2) holding three of the in-flight jobs (the IDs vary), "the accounting
closes exactly" — plus the structural fix. Keep a captured transcript in your back pocket: if the
live demo misbehaves, show the transcript.

## Ask the room

Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- The agent replayed `info threads` + N × `context` to reproduce your
  `goroutines -group userloc`. What's the operational cost of that
  difference at 400 goroutines?

  Two separate costs, and it's worth naming both. The first is wall-clock:
  `goroutines -group userloc` is one CLI invocation with grouping built in —
  Delve does the bucketing locally and hands you back N call sites, not N
  goroutines. The agent has no such primitive. `info threads` gets you the
  list, then it's `context` once per goroutine of interest — at 400
  goroutines that's potentially 400 round-trips, and each one is a model
  turn plus a DAP request/response, not a free function call. That's easily
  minutes of latency for something that took you a few hundred milliseconds
  by hand. Second, and arguably worse: there's no query language underneath
  any of it. You can ask `dlv` "show me only the ones blocked on
  `chansend`"; the agent can't ask DAP that question at all — it has to
  fetch every goroutine's context and do the filtering itself, in its own
  reasoning, which burns both tool calls and context window. The gap isn't
  linear either — grouping turns an O(N) survey into an O(distinct call
  sites) one, and at 400 goroutines those numbers can be very different.

- `evaluate` renders a channel as `chan main.Job 4/4`. What did you get
  from `print jobs` in ex2 that the agent didn't have? (Full `hchan`
  struct: `sendq`/`recvq` membership, `qcount`, `dataqsiz`, buffer
  contents.) Which of those questions could the agent *reconstruct*
  from other tool calls, and which could it not?

  Be precise about what `evaluate` actually hands back: a type and a
  count/capacity pair, `4/4`. That's it — Delve's default rendering for a
  channel value. `print jobs` in ex2 walked the real runtime `hchan`
  struct, which is a lot more than a summary: `qcount` and `dataqsiz` (which
  is where `4/4` comes from — so that part isn't lost), plus `sendq` and
  `recvq`, the actual linked lists of parked goroutines waiting to send or
  receive, and the raw buffer contents.

  Reconstructability splits cleanly in two. The buffer contents the agent
  *can* get — Beat 5 shows it working: evaluate `jobs.buf` directly with a
  valid `frameId` and you get the same `*[4]main.Job` array `print jobs`
  would show you, because that's just another expression in scope. But
  `sendq`/`recvq` membership — which specific goroutines are parked on this
  channel — the agent cannot reconstruct through `evaluate` at all. There's
  no expression that hands back "the goroutines waiting on this channel."
  The only way to get an equivalent fact is indirect: go goroutine by
  goroutine with `context`, look at which ones are sitting in
  `runtime.chansend` or `runtime.chanrecv`, and infer from the call stack
  which channel each one is blocked on. That's exactly the N-times-`context`
  tax from the first question, applied to a narrower query — you're
  rebuilding one field of `hchan` by brute-force cross-referencing instead
  of reading it directly.

- What's the value of the `"do not read any files"` constraint in the
  prompt? What would you learn about the agent by removing it?

  It's the whole demo, really — the doc says as much up front: that
  constraint plus "only the debugger tools" forces the agent to reason from
  the live process instead of pattern-matching on source. It's the same
  discipline the room just practiced by hand for three exercises: you don't
  get to read a comment that says "this blocks here," you have to stop the
  process and look. Without it, an agent with a file-read tool has a much
  easier path available — read `main.go`, see `numWorkers=3` and
  `cap(jobs)=4`, do the arithmetic in its head, and produce a *correct*
  writeup that never touched the debugger meaningfully. That answer would
  look identical to the grounded one in the final report, which is exactly
  the trap: you'd have no way to tell, from the writeup alone, whether the
  agent actually understood `sendq`/`recvq` or just did static analysis and
  narrated debugger calls it didn't need.

  Removing the constraint would tell you something real, just not what you
  want from this exercise. You'd likely see it converge faster — reading
  source is cheaper than N round-trips of `context` — but with a much
  weaker evidence chain: fewer tool calls, more claims that trace back to
  "I read line 44" instead of "goroutine 36 is parked in `runtime.chansend`
  at frame 1011." You'd be learning about its code-reading competence, which
  is a different (and less interesting, for this class) skill than its
  debugging competence. The constraint is what turns this into a debugger
  demo instead of a code-review demo.

- Where would you deploy this workflow first: interactive at your desk,
  or headless in CI on hung integration tests? Why?

  Interactive, at your desk, first — and the doc's own "where a human is
  still faster" list is the reason why. No goroutine query language,
  shallower channel forensics than `print`, no watchpoints over DAP-via-MCP,
  and real per-hop latency. Layer on top of that the failure modes this
  exact exercise documents under Common pitfalls: the agent evaluating with
  no `frameId` and looping on "unable to evaluate expression," or setting
  breakpoints in the worker loop and re-hitting them forever. Those are
  exactly the moments where having a human present to nudge it — "call
  context first," "clear breakpoints and continue to the fatal error" — is
  the difference between a five-minute demo and a stuck session burning
  tool calls with nobody watching.

  Headless-in-CI-on-hung-integration-tests is the higher-value *eventual*
  target — it's precisely the "3am hung binary" scenario the closing frame
  gestures at, `debug {mode: "attach", processId: ...}` producing a
  first-pass triage before anyone's laptop is even open. But that's a
  harder bar: it requires the known failure modes to be solved *unattended*,
  with no one there to type the nudge. So the natural rollout order is
  interactive-with-a-human-nudging now, while those rough edges are still
  being learned; headless-but-narrow next, scoped to a known class of hangs
  where you've already seen the agent's failure modes and can guard against
  them (this exact deadlock shape is a good first candidate, having just
  watched it work); and broad autonomous CI triage only once you trust the
  agent to recover from its own frameId and breakpoint-loop mistakes without
  a human in the loop.

## Common pitfalls

- **`dlv` not on PATH.** The MCP server spawns `dlv dap` and inherits
  the *agent's* environment. If `debug` fails with "dlv not found",
  either launch Claude from a shell where `dlv version` works, or
  re-add the server with an explicit PATH:
  ```bash
  claude mcp add --scope user debugger \
    -e PATH="$PATH" -- $(go env GOPATH)/bin/mcp-dap-server
  ```

- **Tools appear only after the session starts.** Before `debug`, the
  agent sees exactly one tool. Students may panic that "the tools
  aren't there yet". They're not — that's by design; the surface swaps
  from 1 tool to 12 the moment a session is live (`debug` is replaced,
  not joined). Show `/mcp` before and after so the room sees it.

  **Do this before your very first `debug` call** — it's a one-shot. After
  a `stop`, `/mcp` lists 4 tools (`debug`, `disassemble`, `restart`,
  `set-variable`), not 1, so the 1→12 transition only reads cleanly once.

- **`evaluate` at the exception stop with no `frameId`.** Top frame is
  `runtime.gopark`; user variables aren't in scope. The agent must
  call `context` first and pass a user-frame `frameId` to `evaluate`.
  If it loops on `unable to evaluate expression`, nudge:
  *"call context first and pass the frameId of a main.* frame to
  evaluate."*

- **The agent sets breakpoints in the worker loop and re-hits them
  forever.** Nudge: *"clear all breakpoints (`{"all": true}`) and
  continue to the fatal error."* Note `clear-breakpoints` with `{}`
  is an error — it needs `file`, `function`, or `all`.

- **Session wedged.** Have the agent call `stop` (or `restart`) and
  relaunch. Worst case: `/mcp` → reconnect the server → re-prompt with
  "resume: launch the dispatcher again". (Calling `debug` during a live
  session can't happen the way you'd expect — it isn't in the tool list,
  so it comes back as a JSON-RPC protocol error, `unknown tool "debug"`.)

- **The agent starts reading `main.go`.** Deny the file-read permission
  prompt and let the denial teach the lesson — the prompt said
  debugger only. On first use, approve the debugger tools (or "always
  allow") so the flow doesn't stall mid-demo.

- **Leftover `__debug_bin*` binaries.** Source-mode sessions leave
  these in the package directory when Delve dies at the fatal error.
  Gitignored; the repo's `cleanup.sh` sweeps them.

- **Total meltdown.** Fall back to ex2 by hand: *"here's the same
  investigation, four commands, eight seconds."* Honestly a fine
  finale on its own.

## Closing frame

Where the agent is genuinely good:

- Runs the full loop unattended — launch, stop, survey, inspect,
  conclude, write it up.
- Never forgets what it learned within a session: after one `frameId`
  lesson it targets frames correctly for the rest of the run.
- Scales sideways: pointed at a hung binary at 3am with `debug {mode:
  "attach", processId: ...}`, it produces first-pass triage before a
  human's laptop is open.

Where a human driving `dlv` is still faster:

- **No goroutine query language.** DAP has no equivalent of
  `goroutines -group userloc`, `-with label`, or `-chan <expr>`. The
  agent replays that as N × `context` calls and diffs. On 400
  goroutines that gap is enormous.
- **Shallower channel forensics.** `evaluate` gives you
  `chan main.Job 4/4`; `print jobs` gives you the full `hchan` with
  `sendq`/`recvq` *and* the waiting-goroutine summary.
- **No watchpoints over DAP-via-MCP today.** Ex3 stays yours.
- **Latency.** Every hop is a model turn. Your fingers are the fast
  path once you know the next question.

The closing line writes itself: **the agent multiplies the person who
knows what a full `sendq` means. It does not replace them. Someone has
to check the transcript.**
