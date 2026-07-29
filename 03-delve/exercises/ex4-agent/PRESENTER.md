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
  Thread 1:  [Go 1]  main.main
  Thread 2:  [Go 2]  runtime.gopark
  ...
  Thread 19: [Go 19] main.worker
  Thread 20: [Go 20] main.worker
  Thread 21: [Go 21] main.worker
```

DAP "threads" are goroutines under Delve. This is the agent's version
of `goroutines -group userloc` — but without grouping. **Count the
tool calls it burns to get what one CLI command gave you.**

### Beat 4: `context` per goroutine — stacks with frame IDs

`context` with `{}` returns the current goroutine's location + full
stack + all locals. Add `{"threadId": 19}` for another goroutine.

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

where `mcp.json` is
`{"mcpServers":{"debugger":{"command":"<path>/mcp-dap-server"}}}`.

The captured baseline run produces the complete cycle — main parked on
`jobs` (4/4) holding job 10, all three workers parked on `reports`
(2/2) holding jobs 1/4/5, "the accounting closes exactly" — plus the
structural fix. Keep a captured transcript in your back pocket: if the
live demo misbehaves, show the transcript.

## Ask the room

- The agent replayed `info threads` + N × `context` to reproduce your
  `goroutines -group userloc`. What's the operational cost of that
  difference at 400 goroutines?
- `evaluate` renders a channel as `chan main.Job 4/4`. What did you get
  from `print jobs` in ex2 that the agent didn't have? (Full `hchan`
  struct: `sendq`/`recvq` membership, `qcount`, `dataqsiz`, buffer
  contents.) Which of those questions could the agent *reconstruct*
  from other tool calls, and which could it not?
- What's the value of the `"do not read any files"` constraint in the
  prompt? What would you learn about the agent by removing it?
- Where would you deploy this workflow first: interactive at your desk,
  or headless in CI on hung integration tests? Why?

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
  aren't there yet". They're not — that's by design; the surface grows
  from 1 to 13 the moment a session is live. Show `/mcp` before and
  after so the room sees the transition.

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

- **Session wedged / double `debug` call.** Have the agent call `stop`
  (or `restart`) and relaunch. Worst case: `/mcp` → reconnect the
  server → re-prompt with "resume: launch the dispatcher again".

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
