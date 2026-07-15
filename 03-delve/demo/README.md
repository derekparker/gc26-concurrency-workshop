# Demo: Debugging a Pipeline Deadlock with Delve

> **Instructor script.** Every command and output snippet below was captured
> against this exact program with Go 1.26 and Delve 1.27. Goroutine *IDs*
> vary from run to run; everything else is deterministic (the order stream
> uses a fixed seed).
>
> **Timing: ~25–30 minutes** (steps 1–5 are the core, ~20 min; step 6 is a
> tour of extra firepower you can trim if running behind).

## The Program

A four-stage order pipeline connected by **unbuffered channels**:

```
main --SendOrder--> [incoming] --> receiver --> [validation] --> validator
                                --> [processing] --> processor x2
                                --> [shipping]   --> shipper
```

The shipper stops after 10 orders. Main submits 25. Nobody planned for that.

Run it once *without* the debugger:

```bash
cd 03-delve/demo
go run .
```

After ~2.5 seconds of healthy-looking logs:

```
20:27:35.040 [SHIPPER] Shipped 10 orders so far
20:27:35.040 [SHIPPER] Shipped maximum orders (10), shutting down
fatal error: all goroutines are asleep - deadlock!

goroutine 1 [chan send]:
main.(*Pipeline).SendOrder(...)
	.../pipeline.go:164 +0xd8
...four more screens of stack traces...
```

**Teaching beat:** the Go runtime *did* detect this deadlock — but only
because literally every goroutine was asleep. One background ticker, one
open network connection, one `time.Sleep` anywhere, and this program would
have hung silently forever (exercise 1 does exactly that). And even when
you get the dump, it's a wall of text you correlate by hand. Delve gives
you the same information *live*, queryable, with the program frozen so you
can interrogate it.

## Step 1: Run It Under Delve (~3 min)

```bash
go build -gcflags='all=-N -l' -o pipeline .
dlv exec ./pipeline
```

> `dlv debug` compiles with `-N -l` (optimizations off) for you and is fine
> here too; `dlv exec` on a `-N -l` build mirrors how you'd debug a binary
> you built in CI. If you debug a *production* binary built with default
> flags, expect `Warning: debugging optimized function` and some
> `<optimized out>` variables — Delve copes, but locals get fuzzy.

```
(dlv) continue
```

Let it run. After ~2.5s Delve stops **automatically**:

```
20:27:35 [SHIPPER] Shipped maximum orders (10), shutting down
> [runtime-fatal-throw] runtime.fatal() /usr/local/go/src/runtime/panic.go:1241 (hits total:1)
```

Delve pre-sets internal breakpoints on `runtime-fatal-throw` and
`unrecovered-panic`, so a fatal error is not a crash — it's a **breakpoint**.
The process is frozen at the instant of death, with all state intact.

**Teaching beat:** for a program that hangs *without* the runtime noticing
(the common case), you get to this same frozen state by pressing `Ctrl+C`
in Delve, or by attaching to the running process with `dlv attach <pid>`.

## Step 2: Survey the Goroutines (~5 min)

```
(dlv) goroutines -with user
```

```
* Goroutine 1 - User: ./pipeline.go:164 main.(*Pipeline).SendOrder (0x104c040c8) [chan send]
  Goroutine 5 - User: ./pipeline.go:101 main.(*Pipeline).validator.func1 (0x104c03480) [chan send]
  Goroutine 6 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 (0x104c0397c) [chan send]
  Goroutine 7 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 (0x104c0397c) [chan send]
  Goroutine 9 - User: ./pipeline.go:76 main.(*Pipeline).receiver.func1 (0x104c02fa8) [chan send]
[5 goroutines]
```

`-with user` hides runtime housekeeping goroutines (GC, finalizer, ...).
Plain `goroutines` shows everything — worth showing once so students see
what gets filtered.

Read the wait reasons out loud: **every single user goroutine is in
`chan send`**. Even `main` — it never reached its own timeout logic; it's
wedged inside `SendOrder` on order 18.

For programs with hundreds of goroutines, group instead of read:

```
(dlv) goroutines -group userloc
```

```
.../pipeline.go:101 in main.(*Pipeline).validator.func1
	Total: 1
.../pipeline.go:127 in main.(*Pipeline).processor.func1
	Total: 2
.../pipeline.go:164 in main.(*Pipeline).SendOrder
	Total: 1
.../pipeline.go:76 in main.(*Pipeline).receiver.func1
	Total: 1
/usr/local/go/src/runtime/proc.go:463 in runtime.gopark
	Total: 5
```

One line per *place* the program is stuck, with counts. This is the single
highest-value command for concurrency debugging — in a real service this
turns 400 goroutines into 6 lines.

The pipeline also labels its workers with pprof labels
(`pprof.Do(ctx, pprof.Labels("job", "processor"), ...)`), which Delve
understands:

```
(dlv) goroutines -group label job
```

```
job=processor
	Total: 2
job=receiver
	Total: 1
job=validator
	Total: 1
```

**Ask the room:** what's missing from that list? There is no `job=shipper`
group. The shipper is *gone* — remember that for step 5.

(Also available: `goroutines -l` to print labels inline, and
`goroutines -with label job=processor` to filter by label.)

## Step 3: Inspect One Goroutine (~4 min)

```
(dlv) goroutine 1
(dlv) bt
```

```
0  0x000000010303a9c0 in runtime.gopark
   at /usr/local/go/src/runtime/proc.go:463
1  0x0000000102fc94a4 in runtime.chansend
   at /usr/local/go/src/runtime/chan.go:283
2  0x0000000102fc9288 in runtime.chansend1
   at /usr/local/go/src/runtime/chan.go:161
3  0x00000001030980c8 in main.(*Pipeline).SendOrder
   at ./pipeline.go:164
4  0x0000000103098618 in main.main
   at ./pipeline.go:208
```

Move to the first *user* frame and look around:

```
(dlv) frame 3
(dlv) print order.ID
18
```

Main is stuck submitting **order 18 of 25**. You can evaluate any
expression in this goroutine's context — `args`, `locals`, `print p`, all
scoped to frame 3.

> Shortcut: `goroutine <id> <command>` runs a command on another goroutine
> without switching to it, e.g. `goroutine 9 stack`, and
> `goroutines -exec stack 3` runs a command on *every* goroutine (combine
> with filters: `goroutines -with label job=processor -exec stack 3`).

## Step 4: Follow the Channels (~6 min)

We're in `SendOrder`'s frame, so `p` (the pipeline) is in scope. Ask Delve
who is stuck on each channel:

```
(dlv) goroutines -chan p.incoming
* Goroutine 1 - User: ./pipeline.go:164 main.(*Pipeline).SendOrder [chan send]

(dlv) goroutines -chan p.validation
  Goroutine 9 - User: ./pipeline.go:76 main.(*Pipeline).receiver.func1 [chan send]

(dlv) goroutines -chan p.processing
  Goroutine 5 - User: ./pipeline.go:101 main.(*Pipeline).validator.func1 [chan send]

(dlv) goroutines -chan p.shipping
  Goroutine 6 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 [chan send]
  Goroutine 7 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 [chan send]
```

Four commands, and the entire deadlock chain is mapped:

```
main --send--> incoming --receiver--> validation --validator--> processing
                                          --processors(x2)--> shipping --> ???
```

Now print a channel. Delve renders the runtime's internal `hchan` struct
*and* summarizes the waiters:

```
(dlv) print p.shipping
chan main.ProcessedOrder {
	qcount: 0,
	dataqsiz: 0,
	...
	recvq: waitq<main.ProcessedOrder> {
		first: *sudog<main.ProcessedOrder> nil,
		last: *sudog<main.ProcessedOrder> nil,},
	sendq: waitq<main.ProcessedOrder> {
		first: *(*sudog<main.ProcessedOrder>)(0x6a7390c3e070),
		last: *(*sudog<main.ProcessedOrder>)(0x6a7390c3e690),},
	...}

Goroutines waiting on this channel:
  Goroutine 7 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 [chan send]
  Goroutine 6 - User: ./pipeline.go:127 main.(*Pipeline).processor.func1 [chan send]
```

Vocabulary for the room:

- `qcount` / `dataqsiz` — items buffered / buffer capacity (0/0: unbuffered)
- `sendq` / `recvq` — queues of goroutines parked on send/receive (`sudog`
  is the runtime's "goroutine waiting on a synchronization object" record)
- `closed` — 0 or 1

The verdict is written in the struct: **two senders queued, `recvq`
empty**. Nobody will ever receive on `shipping` again.

## Step 5: The Root Cause (~3 min)

Who was supposed to receive on `shipping`? The shipper — which step 2
showed no longer exists. It shipped its self-imposed maximum of 10 orders
and returned (`pipeline.go:141`, `for shipped < 10`). The last log line
before the freeze says exactly that:

```
[SHIPPER] Shipped maximum orders (10), shutting down
```

Chain of causality, backwards from the missing goroutine:

1. shipper exits after 10 orders → nobody receives on `shipping`
2. both processors block sending to `shipping` → nobody receives on `processing`
3. validator blocks sending to `processing` → nobody receives on `validation`
4. receiver blocks sending to `validation` → nobody receives on `incoming`
5. main blocks in `SendOrder` on order 18 → deadlock

**The fixes** (discuss, don't dwell — exercise time is more valuable):

- The real bug is *lifecycle*, not buffering: the shipper must drain the
  channel until it's **closed** (`for order := range p.shipping`), not
  count to an arbitrary 10. Ownership rule: each stage closes its output
  channel when its input is exhausted (receiver already does this
  correctly — walk the `close` chain in the code).
- Buffered channels would only *hide* this bug until order volume grew —
  buffers change *when* senders block, never *whether* a missing receiver
  deadlocks you.
- Real pipelines also want cancellation (a `context.Context` or done
  channel selected in every send) so one dead stage can't wedge the world.

## Step 6: Extra Firepower (~8 min, trim as needed)

Restart the session (`restart` works in `dlv exec`; or just quit and rerun)
and show the *proactive* toolkit — everything so far was post-mortem.

### Conditional breakpoints + attached commands

```
(dlv) break lastorder pipeline.go:150
(dlv) condition lastorder shipped == 9
(dlv) on lastorder print order.ID
(dlv) continue
```

```
> [lastorder] main.(*Pipeline).shipper.func1() ./pipeline.go:150 (hits goroutine(8):1 total:1)
	order.ID: 12
```

Stops exactly on the 10th shipment (order 12 — orders 4 and 11 were
rejected by validation) and auto-prints whatever you attached with `on`.

### Watchpoints — break on *data* instead of *code*

From that stopped frame:

```
(dlv) watch -w shipped
Watchpoint shipped set at 0x3fd32d54fbd8
(dlv) continue
> watchpoint on [shipped] main.(*Pipeline).shipper.func1() ./pipeline.go:151 (hits goroutine(10):1 total:1)
```

Hardware watchpoint: the CPU stops the program on any write to that
address, no matter which goroutine does it. Works on locals, globals,
fields, slice elements. This is the star of exercise 3.

### Tracepoints — printf debugging without the printf

```
(dlv) trace shiptrace main.(*Pipeline).shipper
(dlv) continue
> goroutine(8): [shiptrace] main.(*Pipeline).shipper((*main.Pipeline)(0x1cfe668ca080))
```

Non-stopping breakpoints: a notification per hit, execution continues.
`display -a <expr>` is the complement — auto-print an expression at every
stop.

### `dlv trace` — instrumentation-free function logging

From the shell, no session at all (the argument is a **regexp**):

```bash
dlv trace --timestamp 'SendOrder'
```

```
2026-07-14T16:45:17.062-07:00 > goroutine(1): main.(*Pipeline).SendOrder((*main.Pipeline)(0x73aae55a080), main.Order {ID: 1, Priority: 3, ...})
2026-07-14T16:45:17.081-07:00 >> goroutine(1): main.(*Pipeline).SendOrder => ()
```

Every call, with arguments and return, timestamped, zero code changes.
Watch the trace flow... and then stop, mid-batch — the hang is visible in
the trace itself. (`--follow-calls <depth>` traces callees too.)

### Post-mortem: freeze-dry the deadlock

From any stopped Delve session:

```
(dlv) dump ./pipeline.core
```

Later — different terminal, different day, teammate's machine of the same
platform:

```bash
dlv core ./pipeline ./pipeline.core
(dlv) goroutines -with user      # the whole deadlock, preserved
```

`dlv core` also reads Linux ELF cores (`GOTRACEBACK=crash` + `ulimit -c
unlimited` on a crashed service) and Windows minidumps. On macOS, Delve's
own `dump` is the supported path. Note the dump is a full memory image —
gigabytes for a big process.

## Anticipated Questions

**"My goroutine numbers don't match yours."** Correct — goroutine IDs are
assigned by the runtime and vary per run. Filter and group by *location*
or *label*, never by ID.

**"Why didn't the race detector catch this?"** There's no data race — every
access here is a perfectly synchronized channel operation. Deadlocks,
leaks, and logic bugs are invisible to `-race`. Different tool, different
bug class.

**"Couldn't I see this in the execution tracer?"** You'd see goroutines
blocking and throughput dying, and blocked goroutines' stacks — but the
tracer shows you *events over time*, not queryable state: you can't ask
"who is parked on *this* channel?" or print a channel's `sendq`. Tracer =
what happened; debugger = what is true right now.

**"Does stopping at a breakpoint stop all goroutines?"** Yes. And `next`/
`step` stay pinned to the goroutine you're stepping — if another goroutine
hits a breakpoint meanwhile, Delve tells you and switches. No more "my
print statements interleave" chaos.

**"Can I use this on a running production process?"** `dlv attach <pid>`
— see the section README. Attaching pauses the process only while you're
stopped at a prompt; `quit` offers to leave it running (answer `n` to the
kill prompt, or detach). Be deliberate about stopping the world in prod.

**"What about goroutines that already exited, like the shipper?"** Gone —
a debugger sees present state, not history. If you need history, that's
the execution tracer's job (part II), or a tracepoint set *before* the
exit. Good closing line for the section: the tools compose.
