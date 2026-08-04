# Part III: Debugging Go Concurrency with Delve

## Overview

The race detector told you *that* memory was racy. The execution tracer
told you *what happened over time*. Delve answers the question the other
two can't: **what is true about this program right now?** Which goroutines
exist, what each one is blocked on, what's inside that channel, who holds
that lock, who wrote that value, queryable, live, on a frozen process.

This part covers the Delve workflows that matter for concurrency bugs:
goroutine triage at scale, channel forensics, watchpoints, and debugging
processes you can't restart, live (attach) or dead (core dumps).

**A 2026 note worth saying out loud:** as AI tools write a growing share of
our code, engineers spend a growing share of their time debugging code they
didn't write. A debugger is the fastest way to build ground truth about
unfamiliar code, you don't have to *understand* 400 goroutines to ask
Delve which six places they're stuck in. And it cuts both ways: agents can
drive Delve too (headless RPC / DAP), and a debugger transcript, "these
goroutines, parked on this channel, holding these values", is exactly the
kind of high-signal evidence that keeps an AI assistant fixing the real
bug instead of guessing. The engineer who can produce that evidence stays
the one steering.

## Session Plan (~90 min core + optional finale)

| Time | What | Where |
|------|------|-------|
| 0:00–0:10 | Setup check + Delve crash course (below) | this README |
| 0:10–0:40 | Demo: pipeline deadlock | [`demo/`](demo/) |
| 0:40–1:00 | Exercise 1: the silent stall (+ Go 1.26 leak-profile stretch) | [`exercises/ex1-fanout-fanin/`](exercises/ex1-fanout-fanin/) |
| 1:00–1:15 | Exercise 2: channel forensics | [`exercises/ex2-dispatcher/`](exercises/ex2-dispatcher/) |
| 1:15–1:30 | Exercise 3: watchpoint stakeout (stretch/take-home if short) | [`exercises/ex3-inventory/`](exercises/ex3-inventory/) |
| +15–20 | **Bonus finale (flex block, if time allows):** an AI agent drives Delve over MCP | [`exercises/ex4-agent/`](exercises/ex4-agent/) |

The "Beyond the terminal" section at the bottom (attach, cores, scripting,
editors, agents) is designed to be woven into the demo and wrap-up rather
than presented as its own block. The finale is the workshop's closing act,
it cashes in the AI framing above with a live agent at the debugger,
but the 90-minute core stands alone if the clock says no.

## Setup

Use **Delve 1.27.0 or newer** (current release, 2026-06-19). This material
was verified with Delve 1.27.0 on Go 1.26.

```bash
go install github.com/go-delve/delve/cmd/dlv@latest
dlv version
```

Delve enforces a supported Go range and refuses to run outside it:

| Delve | Supports Go |
|-------|-------------|
| 1.26.0 – 1.26.3 | 1.24 – 1.26 |
| **1.27.0** | **1.25 – 1.27** |

So 1.26.0 is the *minimum* for Go 1.26, but **only 1.27.0 handles Go 1.27** —
which matters, because Go 1.27 is expected in August 2026. On a mismatch you
get a hard error, not a warning:

```
Version of Delve is too old for Go version go1.27
(maximum supported version 1.26, suppress this error with --check-go-version=false)
```

`--check-go-version=false` downgrades it to a warning, but "undefined
behavior" is exactly what it says: wrong variable values, broken stacks.
Upgrade instead.

`./setup.sh` checks this for you and upgrades a too-old Delve, so re-run it
if it's been a while since you set up.

Three ways to start a session, all used in this part:

```bash
dlv debug            # compile current package with -N -l and debug it
dlv exec ./binary    # debug a prebuilt binary
dlv attach <pid>     # debug an already-running process
```

For binaries you build yourself, keep variables inspectable:

```bash
go build -gcflags='all=-N -l' -o program .
```

(`-N` no optimizations, `-l` no inlining, what `dlv debug` does for you.)

Watch out for `-trimpath`, which release builds often set: it strips the
absolute paths Delve uses to find your source, so you get a session with no
source listing. Delve 1.26.1+ detects this and warns you rather than leaving
you to guess.

## Delve Crash Course (10 minutes, live at the prompt)

Assume the room knows `break`, `continue`, `next`, `print` from *any*
debugger. Spend the ten minutes on what's Go-specific. The five commands
that solve most concurrency bugs:

```
goroutines -with user            # every user goroutine + its wait reason
goroutines -group userloc        # 400 goroutines -> 6 groups with counts
goroutines -chan <expr>          # who is parked on THIS channel?
goroutine <id> stack             # stack of any goroutine, no switching
print <ch>                       # channel internals: buffer, sendq, recvq
```

Worth demonstrating once each:

- **Wait reasons**, `goroutines` output ends in `[chan send]`,
  `[sync.Mutex.Lock]`, `[select]`, `[sleep]`, `[IO wait]`... Triage by
  wait reason before reading a single line of code.
- **Filtering/grouping**, `-with user`, `-with/-without (userloc|curloc|goloc|startloc) <substr>`,
  `-with/-without running`, `-group (userloc|curloc|goloc|startloc|running|user)`,
  `-group label <key>`, `-with label key=value`, `-exec <cmd>`
  to run a command on every match. If your services set pprof labels
  (`pprof.Do`), Delve turns them into a query language for goroutines.
  One syntax gotcha: the expression in `-chan <expr>` **must not contain
  spaces**.
- **Context switching**, `goroutine 7` makes goroutine 7 current
  (`bt`, `frame 3`, `locals`, `print` all follow); `goroutine 7 stack`
  peeks without switching.
- **Panics and fatal errors are breakpoints**, Delve pre-installs
  `unrecovered-panic` and `runtime-fatal-throw`, so `fatal error: all
  goroutines are asleep - deadlock!` leaves you at a prompt with the
  corpse still warm. For hangs the runtime *doesn't* detect: `Ctrl+C`
  freezes the process at a prompt.
- **Stepping is goroutine-pinned**, breakpoints stop the world; `next`
  and `step` stay on your goroutine rather than bouncing to whichever
  goroutine runs next.
- **Breakpoints that do things**, `condition <bp> <expr>`,
  `on <bp> <command>`, `trace <locspec>` (notify, don't stop),
  `display -a <expr>`, `watch [-r|-w|-rw] <expr>` (hardware watchpoint on a
  memory location, break on *data*, not code). Worth knowing before
  exercise 3: **writes that don't change the value may not be reported**, so
  a watchpoint catches the write that *changed* your variable, not every
  write that touched it.

## What Each Piece Teaches

- **`demo/`**, a pipeline deadlock, guided walkthrough: goroutine triage,
  the `-chan` filter, channel internals, root-causing a missing receiver,
  plus conditional breakpoints, tracepoints, `dlv trace`, and `dump`.
- **`ex1-fanout-fanin/`**, a fan-out/fan-in pipeline that stalls
  *silently* (the runtime deadlock detector never fires). Attach or
  interrupt, group by location, find the one goroutine that's different.
  Its stretch goal demonstrates the **goroutine leak profile** against the
  same stall (see below).
- **`ex2-dispatcher/`**, a dispatch/collect deadlock. Solved almost
  entirely by reading `print <chan>` output: full buffer, empty `recvq`,
  loaded `sendq`.
- **`ex3-inventory/`**, a mutex-protected, race-detector-clean,
  wrong-*value* bug. A watchpoint catches the culprit goroutine mid-write.
  The hardest and the most "only a debugger finds this" of the three.
- **`ex4-agent/`** (bonus finale), no new program; students wire
  [`go-delve/mcp-dap-server`](https://github.com/go-delve/mcp-dap-server)
  into a coding agent and watch it debug ex2's deadlock live: launch,
  goroutine sweep, frame-targeted evaluation, written verdict.

## Beyond the Terminal

### Debugging a live process: `dlv attach`

```bash
dlv attach $(pgrep myservice)
(dlv) goroutines -group userloc     # why is it stuck / leaking?
```

The process is only paused while you sit at the prompt; `quit` asks
whether to kill it, answer `n` to detach and let it keep running. This is
the move when a service is wedged *right now* and a restart would destroy
the evidence. (On Linux boxes you may need
`sysctl kernel.yama.ptrace_scope=0` or root; on macOS, Delve's installed
debugserver handles the signing dance.)

### Post-mortem: core dumps

Deadlocked service, can't keep it paused? Freeze-dry it:

```bash
# Option A, from a live Delve session (any platform):
(dlv) dump ./service.core

# Option B, on Linux: make fatal errors leave a core behind
ulimit -c unlimited
GOTRACEBACK=crash ./service      # fatal error/panic now aborts with a core

# Either way, debug the corpse later, anywhere with the same binary:
dlv core ./service ./service.core
(dlv) goroutines -with user      # the deadlock, preserved forever
```

`dlv core` reads Linux ELF cores, Windows minidumps, and Delve `dump`
files. Everything read-only works: goroutines, stacks, `print`, `-chan`.
You obviously can't `continue`. Great for CI: capture cores from hung
integration tests and debug them after the fact.

### Scripting Delve

Non-interactive sessions for repeatable investigations (and for agents):

```bash
# run a canned command list
dlv exec ./service --init deadlock.dlv

# lightweight function tracing, no code changes, args + returns + timestamps
dlv trace --timestamp 'SendOrder|Checkout'

# -v controls how much of each parameter you see:
#   0=values (default) 1=types 2=inline 3=expanded 4=full
dlv trace -v 3 --timestamp 'SendOrder'

# follow callees too, to the given depth
dlv trace --follow-calls 2 'Checkout'
```

Inside a session, `transcript <file>` appends command output to a file
(`-t` truncates first, `-x` suppresses stdout, `transcript -off` stops).
That's the literal mechanism behind "hand the debugger transcript to your
AI assistant" below, and it's equally good for attaching evidence to a bug
report.

Delve also embeds Starlark (`source script.star`) for programmatic
sessions, e.g. iterate all goroutines and dump selected frame variables.

### Editors and DAP

`dlv dap` speaks the Debug Adapter Protocol; VS Code's Go extension,
GoLand, and Neovim (nvim-dap) all sit on top of it. Same engine, so
everything from this section exists there too, VS Code's CALL STACK view
is `goroutines`, conditional breakpoints are right-click away, and the
DEBUG CONSOLE accepts `dlv` expressions.

DAP has been where most recent Delve work has landed, and it has closed
much of the gap with the CLI:

- **Data breakpoints** (1.26.2), the DAP name for watchpoints. The
  exercise 3 technique is now a right-click in the Variables pane.
- **Suspended breakpoints** and multi-process **`follow-exec`** targets (1.26.0)
- Hit-conditional breakpoints (1.26.1), read (1.26.0) and write (1.27.0)
  memory, `examinemem` in the debug console (1.26.2)

Use the GUI day-to-day if you like, but the CLI is what you have over SSH
on the box where prod is burning, and it's what scripts and agents can
drive.

### The goroutine leak profile

Go adds a pprof profile that reports only goroutines the GC can *prove*
will never wake up. Read it as `runtime/pprof.Lookup("goroutineleak")`
or `/debug/pprof/goroutineleak`.

- **Go 1.26**: experimental, gated at build time. Build with
  `GOEXPERIMENT=goroutineleakprofile` or the endpoint 404s.
- **Go 1.27**: generally available. The `goroutineleakprofile`
  GOEXPERIMENT setting is deleted; the profile is simply there.

It's the fleet-scale complement to this section: the profile tells you
**that** goroutines leaked and where they're parked; Delve tells you
**why**. Exercise 1's stretch goal runs it against the silent stall,
real output included,
[see it there](exercises/ex1-fanout-fanin/README.md#stretch-the-goroutine-leak-profile).

### AI agents can hold the debugger too

```bash
dlv debug --headless --listen=127.0.0.1:2345 --accept-multiclient
dlv connect 127.0.0.1:2345    # a human... or not
```

Headless Delve exposes the full JSON-RPC API (and `dlv dap` the DAP one);
the Delve project ships an MCP bridge,
[`go-delve/mcp-dap-server`](https://github.com/go-delve/mcp-dap-server),
that lets coding agents set breakpoints and walk goroutine dumps through
those same protocols. The bonus finale,
[`exercises/ex4-agent/`](exercises/ex4-agent/), wires it into Claude Code
and lets an agent debug exercise 2's deadlock in front of the room. An
agent that can walk a frozen process reasons from evidence; an agent that
can't is pattern-matching on your source. Either way, *someone* has to
know what a `sendq` full of parked goroutines means, that's you.

You don't need the MCP bridge to get most of the benefit, either: run
`transcript session.txt` at the start of a session and everything you do is
captured to a file. That file, pasted into any assistant, is the same
high-signal evidence.

(`mcp-dap-server` remains pre-release with no tagged versions and is under
active development, so pin nothing and re-verify against whatever `@latest`
is the week you use it.)

## Verify Your Setup

```bash
cd 03-delve/demo && go build -o /dev/null ./... && dlv version
```

## Additional Resources

- [Delve CLI command reference](https://github.com/go-delve/delve/blob/master/Documentation/cli/README.md)
- [Delve documentation tree](https://github.com/go-delve/delve/tree/master/Documentation)
- [`goroutines` command deep dive](https://github.com/go-delve/delve/blob/master/Documentation/cli/README.md#goroutines), filtering, grouping, `-chan`, `-exec`
- [go-delve/mcp-dap-server](https://github.com/go-delve/mcp-dap-server), the MCP ↔ DAP bridge used in the bonus finale
- [Go 1.26 release notes: goroutine leak profile](https://go.dev/doc/go1.26), the `goroutineleak` profile used in exercise 1's stretch goal
