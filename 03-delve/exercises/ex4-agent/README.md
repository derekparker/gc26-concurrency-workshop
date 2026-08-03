# Bonus Finale: An AI Agent Holds the Debugger

> **Instructor-led, if time allows. Time: ~15–20 minutes.**
>
> This is the payoff of the framing we opened the section with: agents can
> drive Delve too. Students connect Delve's MCP bridge to a coding agent
> and watch the agent debug a live deadlock, set up a session, walk the
> goroutines, read the channels, explain the cycle, using the exact
> evidence-gathering moves *they* just practiced by hand.
>
> **There is no new program in this exercise.** The debuggee is
> [`ex2-dispatcher`](../ex2-dispatcher/), its deadlock is fully
> deterministic, which is what makes this demo presenter-proof. Everything
> below was captured for real with `mcp-dap-server` (pseudo-version
> `v0.0.0-20260723000135-ca7f841a8ab2`, verified 2026-07-31), Delve 1.27.0,
> Go 1.26, and Claude Code 2.1.x.

## What This Is

[`github.com/go-delve/mcp-dap-server`](https://github.com/go-delve/mcp-dap-server)
is a bridge maintained under the Delve org:

```
agent (Claude Code, ...)  --MCP/stdio-->  mcp-dap-server  --DAP-->  dlv dap  --> your process
```

The agent speaks **MCP** (the standard way agents call tools); the server
translates each tool call into **DAP** requests (the same protocol VS
Code and GoLand sit on, see the section README) against a `dlv dap`
instance it spawns. Nothing here is agent-magic: every tool maps to
debugger operations you've already used this session.

The tool surface is deliberately small and **dynamic**: before a session
starts the agent sees exactly one tool, `debug` (launch from source or
binary, open a core dump, or attach to a PID, Delve for Go, GDB for
C/C++/Rust). The moment a session is live, `debug` is *replaced* by twelve
session tools:

```
breakpoint, clear-breakpoints, continue, step, pause, restart, stop,
context, evaluate, set-variable, info, disassemble
```

`context` is the workhorse: location + full stack (with frame IDs) + all
locals in one call. `info {"type": "threads"}` lists goroutines
(DAP "threads" are goroutines under Delve). `evaluate` takes an optional
`frameId` so expressions run in a *user* frame, remember that; it's the
demo's best teaching moment when the agent gets it wrong.

## Setup (Do This Before the Session)

```bash
# 1. The bridge (needs Go >= 1.26.1; the toolchain auto-download handles it)
go install github.com/go-delve/mcp-dap-server@latest

# 2. dlv must be on PATH, the server spawns `dlv dap` for you
dlv version

# 3. Wire it into Claude Code (user scope so it works in any directory)
claude mcp add --scope user debugger -- $(go env GOPATH)/bin/mcp-dap-server
claude mcp list        # should show: debugger ... - ✓ Connected
```

Inside a Claude Code session, `/mcp` shows the server and its (initially
one) tool. Note there are **no tagged releases yet**, `@latest` is a
pseudo-version of `main`; re-run the `go install` the week of the
workshop and re-verify the demo.

## The Live Demo, Scripted

Start Claude Code **in the ex2 directory** (so the agent's cwd is the
module) and paste a prompt like:

> This Go program (main.go in the current directory) deadlocks every
> run. Using only the debugger MCP tools, launch with the `debug` tool
> in source mode, path `<absolute path to ex2-dispatcher>`, run it
> until it stops, then determine which channel operation every user
> goroutine is stuck on and explain the deadlock cycle. Report
> goroutine-by-goroutine evidence. Do not read or modify any files.

The two constraints are the whole trick: *"only the debugger tools"* and
*"do not read any files"* force the agent to reason from the process, not
pattern-match on source, the same discipline this section has been
teaching humans.

What a correct run looks like (verified sequence; narrate each beat):

**1. `debug`** `{mode: "source", path: ".../ex2-dispatcher", stopOnEntry: true}` →

```
Stopped at program entry. Set breakpoints and use 'continue' to reach your code.
```

(Delve compiled the module with `-N -l` behind the scenes, same as
`dlv debug`.)

**2. `continue`**, no breakpoints needed; the runtime's fatal throw *is*
the breakpoint, exactly like your `dlv debug` runs:

```
Stopped: exception
Function: runtime.fatal
File: /usr/local/go/src/runtime/panic.go:1241
```

**3. `info`** `{"type": "threads"}`, the goroutine survey:

```
Threads:
  Thread 1: [Go 1] main.main
  Thread 2: [Go 2] runtime.gopark
  ...
  Thread 35: [Go 35] main.worker
  Thread 36: [Go 36] main.worker
  Thread 37: [Go 37] main.worker
```

The deadlock is deterministic; the goroutine IDs are not. Across runs the
three workers landed on 35/36/37 and 7/8/9 — read them off `info threads`
rather than assuming.

**4. `context`** per goroutine, `{}` for the current one (main), then
`{"threadId": <one of the main.worker thread IDs>}`. Each returns a stack
with frame IDs:

```
#1 (Frame ID: 1009) runtime.chansend at /usr/local/go/src/runtime/chan.go:283
#3 (Frame ID: 1011) main.main at .../ex2-dispatcher/main.go:79
```

`runtime.chansend` under `main.main:79`, and under every `main.worker` at
`main.go:44`, all four user goroutines are blocked **sending**.

**5. `evaluate`** with a user `frameId`, the channel forensics:

```
{"expression": "job",      "frameId": 1011} → main.Job {ID: 10, Target: "pkg/service10"}
{"expression": "jobs",     "frameId": 1011} → chan main.Job 4/4
{"expression": "jobs.buf", "frameId": 1011} → *[4]main.Job [{ID: 8, ...},{ID: 9, ...},{ID: 6, ...},{ID: 7, ...}]
{"expression": "reports",  "frameId": 1017} → chan<- main.Report 2/2
```

Both buffers full, main wedged holding job 10, workers wedged holding
their reports, the agent now has every fact it needs to write up the
cycle from exercise 2, and a good one will do exactly that.

## What to Tell Students to Watch For

- **Which breakpoints it chooses**, or doesn't: does it realize the
  fatal error already stops the process, or does it waste turns setting
  breakpoints in the worker loop first?
- **The goroutine sweep**, `info threads` then per-thread `context` is
  the agent's version of your `goroutines -group userloc` + `stack`.
  Count the tool calls it burns to get what one CLI command gave you.
- **The first `evaluate` failure.** At the exception stop the top frame
  is `runtime.gopark`, so a frame-less `evaluate {"expression": "jobs"}`
  returns `unable to evaluate expression`. Watch whether the agent
  diagnoses the scope problem and reaches for `frameId` on its own,
  this is the "does it actually understand the debugger?" moment.
- **Evidence vs. vibes** in the final answer: does every claim trace to
  a tool result? That transcript-as-proof is the section's closing theme.
- Tool-permission prompts: approve the debugger tools on first use (or
  pick "always allow") so the flow doesn't stall mid-demo.

## When It Goes Sideways (Live Recovery)

All of these are recoverable without restarting the room:

- **`debug` fails: `dlv` not found.** The server inherits the *agent's*
  PATH. Launch `claude` from a terminal where `dlv version` works, or
  re-add with an explicit env:
  `claude mcp add --scope user debugger -e PATH="$PATH" -- $(go env GOPATH)/bin/mcp-dap-server`.
- **Endless `unable to evaluate expression`.** The agent is evaluating in
  a runtime frame. Nudge: *"call context first and pass the frameId of a
  main.* frame to evaluate."*
- **It set a breakpoint in the worker loop and keeps re-hitting it.**
  Nudge: *"clear all breakpoints (`{\"all\": true}`) and continue to the
  fatal error."* (`clear-breakpoints` with `{}` is an error, it needs
  `file`, `function`, or `all`.)
- **Session wedged.** Have the agent call `stop` (or `restart`) and
  relaunch. Worst case: `/mcp` → reconnect the server, then re-prompt with
  "resume: launch the dispatcher again".
- **It starts reading main.go.** Deny the permission prompt and let the
  denial do the teaching, the prompt said debugger only.
- **Total meltdown.** Fall back to exercise 2 by hand: *"here's the same
  investigation, four commands, eight seconds"*, which is honestly a
  fine finale too.

Housekeeping: source-mode sessions leave Delve's `__debug_bin*`
binaries in the package directory when the session dies at the fatal
error (observed with the current build). They're gitignored, and the
repo's `cleanup.sh` sweeps them.

Dry-run the full loop on the venue machine the morning of. `@latest`
moves; this repo's transcript is your known-good baseline.

## Also Works Headless

The same wiring runs non-interactively, CI for "can an agent triage our
hung service?":

```bash
echo "This program deadlocks... (prompt as above)" | \
  claude -p --mcp-config mcp.json --strict-mcp-config \
  --allowedTools "mcp__debugger"
```

`mcp.json` is committed alongside this README and resolves
`mcp-dap-server` off your `PATH`. If `$(go env GOPATH)/bin` isn't on your
`PATH`, replace `"command"` with the absolute path.

Verified against this exact exercise: the one-shot run came back with the
complete cycle, main parked on `jobs` (4/4) holding job 10, all three
workers parked on `reports` (2/2) holding three of the in-flight jobs (the
IDs vary), "the accounting closes exactly", plus the structural fix, and it even told the two
channels apart by their lock addresses in the `gopark` frames, a trick
nobody demonstrated for it. Keep a captured run in your back pocket: if
the live demo misbehaves, show the transcript.

## Discussion: Agent vs. Human at the Prompt

Where the agent is genuinely good:

- **It runs the full loop unattended**, launch, stop, survey, inspect,
  conclude, and narrates a tidy, evidence-cited writeup at the end.
- It never forgets what it learned: after one `frameId` lesson it targets
  frames correctly for the rest of the session.
- It scales *sideways*: pointed at a hung binary at 3am with `debug
  {mode: "attach", processId: ...}`, it produces the first-pass triage
  before a human's laptop is open.

Where you, driving `dlv`, are still faster:

- **No goroutine query language.** DAP has no equivalent of
  `goroutines -group userloc`, `-with label`, or `-chan <expr>`. The
  agent replays that as N×(`context`) calls and diffing, you do it in
  one command. On 400 goroutines that gap is enormous.
- **Shallower channel forensics.** `evaluate` renders a channel as
  `chan main.Job 4/4`; the CLI's `print jobs` gives you the full `hchan`
  with `sendq`/`recvq` *and* the waiting-goroutine summary. The agent
  infers "nobody is receiving" from stacks; you read it off the struct.
- No watchpoints over DAP-via-MCP today, exercise 3 remains yours.
- Latency: every hop is a model turn. Your fingers are the fast path
  when you already know the next question.

The closing line writes itself: the agent multiplies the person who
knows what a full `sendq` means, it does not replace them. Someone has
to check the transcript.
