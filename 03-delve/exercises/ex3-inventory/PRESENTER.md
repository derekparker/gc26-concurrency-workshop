# Presenter: The Watchpoint Stakeout

> Presenter-only. Students use README.md.

## Goal
Teach hardware watchpoints (`watch -w`) as the tool for
race-detector-clean, mutex-protected, *wrong-value* bugs. Students
should leave able to pick a stakeout target, install the watchpoint,
read the hit stream, and identify the corrupting write and its stack.
Often assigned as a take-home if the clock is tight — mention that up
front.

## Reproduce
From the exercise directory:

```bash
cd 03-delve/exercises/ex3-inventory
go run -race .
```

Race detector: silent. Reconciliation: not silent.

```
initial stock: 600 units
orders shipped: 288 units
expected on shelves: 312 units
actual on shelves:   342 units
  [0] gopher-plush    stock= 39 reserved=0
  [1] gopher-mug      stock= 36 reserved=0
  [2] gopher-tee      stock= 36 reserved=0
  [3] gopher-cap      stock= 36 reserved=0
  [4] gopher-pin      stock= 96 reserved=0     <- varies run to run (96-108)
  [5] gopher-sticker  stock= 99 reserved=0
FAILED: inventory drift of +30 units
```

The exact drift is timing-dependent, but the *bug* triggers every run
because the janitor's 300ms scan overlaps the pickers' ~400ms batch by
construction.

**Say out loud:** in this system, stock can only ever go *down* (orders
take units; the janitor merely returns units a reservation already
took). Yet the shelves ended up 30 units rich. Someone is writing a bad
value into `store.stock` — while holding the lock, which is why `-race`
approves.

## Root cause
`Warehouse.releaseExpired` reads a **snapshot** of `stock` and
`reserved`, unlocks, sleeps 300ms simulating a ledger scan, re-locks,
then writes back:

```go
w.stock[item] = stock[item] + reserved[item]   // main.go:92
```

`stock[item]` here is the *snapshot* value from 300ms ago. Any sales
that happened during those 300ms (`Take` / `Reserve` decrementing
`w.stock[item]`) are silently overwritten by the stale absolute. Every
call to `releaseExpired` erases the pickers' progress on any item that
had reservations queued.

**Deeper lesson (drop this at the end):** *snapshot + slow work +
write-back of absolutes* is a stale read-modify-write, even when every
individual access is locked. Locks make accesses atomic; they don't
make *reasoning across time* atomic. The race detector checks
happens-before on accesses, so it can't see this. The CPU saw the
write, though — and the watchpoint made it testify.

## Walkthrough

### 1. Choose a stakeout target

Watchpoints fire on **every** write. Pick a slot with (a) a wrong final
value and (b) few legitimate writes, so you're not stopping every 5ms.
Look at `buildOrders`: the sticker (item 5) is a slow mover — five
orders in the whole batch, yet it ends at 99. Napkin math:
100 − 3 takes − 2 reserves + 2 returns = 97. It shows 99. Watch item 5.

**Ask the room first:** "which slot would *you* watch, and why?"
Reasoning about the picking is the point of this beat.

### 2. Install the watchpoint

`store` is a package-level variable — in scope the moment `main` starts.

```
dlv debug
(dlv) break main.main
(dlv) continue
(dlv) watch -w store.stock[5]
Watchpoint store.stock[5] set at 0x...
(dlv) continue
```

`-w` = stop on writes (`-r`, `-rw` also exist). Hardware watchpoint:
the CPU traps the write no matter which goroutine, function, or pointer
alias performs it. You get 4 of them (amd64 and arm64 both).

Optionally arm auto-print so the value shows on every stop:

```
(dlv) display -a store.stock[5]
```

### 3. Read the hits — the value only ever goes down… until it doesn't

Verified sequence (goroutine IDs will vary):

```
> watchpoint on [store.stock[5]] main.main() ./main.go:138 (hits goroutine(1):1 total:1)
=> 138:		store.stock[i] = initialPerItem          <- setup, expected

(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).Take() ./main.go:55 (hits goroutine(23):1 total:2)
(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).Reserve() ./main.go:64 (hits goroutine(22):1 total:3)
(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).Reserve() ./main.go:64 (hits goroutine(20):1 total:4)
(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).Take() ./main.go:55 (hits goroutine(23):2 total:5)
(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).Take() ./main.go:55 (hits goroutine(22):2 total:6)
(dlv) print store.stock[5]
95                                    <- 5 legitimate ops: 100 -> 95. so far so good
(dlv) continue
> watchpoint on [store.stock[5]] main.(*Warehouse).releaseExpired() ./main.go:93 (hits goroutine(19):1 total:7)
(dlv) print store.stock[5]
99                                    <- WENT UP BY 4. caught red-handed.
(dlv) stack 3
0  0x... in main.(*Warehouse).releaseExpired
   at ./main.go:93
1  0x... in main.main.func1                       <- the janitor goroutine
   at ./main.go:160
2  0x... in runtime.goexit
   at /usr/local/go/src/runtime/asm_arm64.s:1447
```

Note: Delve reports the line *after* the write. The offending
assignment is `main.go:92`:

```go
w.stock[item] = stock[item] + reserved[item]
```

### 4. Verify the reasoning at the frame

From the janitor's frame you can inspect the stale snapshot side by side
with live state:

```
(dlv) frame 0
(dlv) print stock[5]
97                <- snapshot: what stock was 300ms ago
(dlv) print reserved[5]
2                 <- 2 units held in reservation
(dlv) print w.stock[5]
99                <- the stale write already landed: 97 + 2
```

`99` is the damage, not the setup: the watchpoint fires *after* the store
on line 92, so by the time you have a prompt the overwrite has happened.

**To show the `95` that got erased, stop before the write instead.** This is
also more reliable than counting watchpoint hits, the janitor's hit ordinal
shifts run to run, but the condition below is deterministic:

```
(dlv) break bw main.go:92
(dlv) condition bw item == 5
(dlv) continue
(dlv) print stock[5]      # 97 — snapshot from 300ms ago
(dlv) print reserved[5]   # 2  — units held in reservation
(dlv) print w.stock[5]    # 95 — live: two more sales during the scan
```

Now the arithmetic is on screen before it executes: the janitor is about to
write `97 + 2 = 99` over a live value of `95`, erasing both sales. Step once
and `w.stock[5]` is `99`. Same story, higher volume, on items 4 and 0–3:
+30 units of phantom inventory.

## Fix

Write an *increment* of live state, not an overwrite from stale state:

```go
w.mu.Lock()
defer w.mu.Unlock()
for item := range numItems {
	if reserved[item] == 0 {
		continue
	}
	w.stock[item] += reserved[item]      // adjust live state
	w.reserved[item] -= reserved[item]
}
```

The snapshot's `stock` array is now unused; the compiler will insist:

```go
_, reserved := w.snapshot()
```

`reserved[item]` (the snapshot count of what to release) is still
correct to use as the amount: reservations only *grow* between snapshot
and write-back, and releasing only the snapshotted ones is the intended
semantics. The bug was overwriting `stock` with a stale absolute.

The whole fix is in `solution.diff`, applied from this directory:

```bash
git apply solution.diff        # snapshot stock discarded, increment instead of overwrite, exactly as above
```

> Undo it with `git apply -R solution.diff` when you want the drifting
> version back for the next session.

Verify:

```bash
go run -race .
```

```
...
SUCCESS: books balance
```

## Ask the room

- Watchpoints fire on **every** write. What made this program
  stake-out-friendly? What would you do in a system where the hot item
  gets 1,000 writes/sec?

  Two things were engineered into this exercise on purpose. First,
  `numPickers` is only 4, each picker sleeps 5ms per order, so writes to
  any one item are sparse in real time. Second, `buildOrders` deliberately
  makes item 5 (the sticker) a slow mover — only five orders touch it in
  the whole 320-order book (indices 15, 59, 119, 260, 280) — so
  `watch -w store.stock[5]` fires half a dozen times total, cheap to
  single-step through by hand. Watch a fast mover like item 0 instead
  (routed to `k % 5`, four items sharing the bulk of 320 orders) and
  you're stepping through dozens of legitimate writes before the
  janitor's write ever shows up — see "Common pitfalls" below.

  At 1,000 writes/sec on the hot item, an unconditional `watch -w` is
  unusable — you'd spend the session mashing `continue`. The moves: put a
  `condition` on the watchpoint (it gets an ordinary breakpoint ID, so
  `condition <id> <expr>` applies same as any breakpoint — e.g. skip hits
  from goroutines you already know are legitimate pickers); watch a
  *rarely-written aggregate* instead of the hot field itself — here that
  would look like adding a `releaseCount`/`lastReleaseAmount` field the
  janitor alone touches, and watching that instead of `store.stock[5]`
  directly; use `-r` (read-only) on a sentinel that only the suspect code
  path touches, when the write itself is too hot but something downstream
  of it is quiet; or sample by goroutine — break inside the suspect
  goroutine (the janitor) specifically and inspect state there, rather
  than watching the shared word every goroutine hammers. The common
  thread: stop watching what everyone writes, start watching something
  only the suspect writes.

- The drift (+30) is timing-dependent, but the bug triggers every run.
  What structural property guarantees that?

  The janitor's ticker fires every 250ms
  (`time.NewTicker(250 * time.Millisecond)`, main.go:155), and each call
  to `releaseExpired` then sleeps 300ms (main.go:83) before writing back —
  so the snapshot-to-write-back window is at least 300ms long, and the
  *first* tick always lands at t=250ms. Meanwhile the whole picking phase
  (320 orders / 4 pickers x 5ms/order) takes roughly 400ms wall-clock to
  drain the channel. 250ms falls inside that 400ms window, and
  250+300=550ms lands well past it — so the first `releaseExpired` call is
  structurally guaranteed to snapshot stock *while pickers are still
  running* and write it back *after they've finished*, silently erasing
  whatever sales landed in between. There's no race to win here: the
  numbers are chosen so the overlap happens on every single run regardless
  of scheduler luck. Only the *amount* of drift is timing-dependent — how
  many sales happen to fall inside that particular 250–550ms window, and
  how many of the items with open reservations get caught in it — which is
  why the total floats around +30 (item 4 alone varies 96–108) rather than
  landing on a fixed number.

- Could you have found this with the execution tracer? With logging?
  What's the *minimal* evidence that convicts the janitor, and which
  tool produces it fastest?

  The execution tracer (`go tool trace`, section 02) shows you *when*
  goroutines run and block, not what they write. It would show the
  janitor goroutine parked in `time.Sleep` for 300ms inside
  `releaseExpired`, and the picker goroutines actively running
  `Take`/`Reserve` during that same window — real, visible overlap.
  That's genuinely useful: it tells you *this* goroutine was asleep across
  a window in which *those* other goroutines mutated shared state, which
  is the shape of the bug. But it stops at scheduling. The trace view has
  no idea a `[numItems]int64` snapshot went stale or that a write silently
  reverted four sales; you'd be staring at overlapping bars and
  *inferring* a state bug from a timing coincidence, not seeing one
  directly. To close that gap you'd need to hand-instrument — add a trace
  region or `trace.Log` call that emits `stock[5]` at snapshot time and
  again at write-back time — at which point you're doing the watchpoint's
  job by hand, with extra ceremony.

  Logging has the mirror-image problem. Log every read and write to
  `store.stock` and at this program's write-rate (four pickers, one write
  every 5ms apiece, plus a janitor write every scan) you either drown in
  volume trying to spot the one bad write, or you already suspect
  `releaseExpired` and add a targeted log line there — in which case
  you've already solved the case and the log is just confirmation.
  Logging is a *confirmation* tool once you have a hypothesis; it's a poor
  *discovery* tool when you don't have one yet.

  The watchpoint beats both because it doesn't ask you to correlate timing
  with value, or guess what to log — it triggers exactly and only on
  writes to `store.stock[5]`, and it hands you the stack of the actual
  offending write (`main.(*Warehouse).releaseExpired`, main.go:93, called
  from the janitor goroutine) the moment a wrong write happens. That's the
  minimal evidence that convicts: one hit, one stack trace, no
  correlation step required. Tracer and logging can corroborate
  afterward; the watchpoint is what gets you there first.

- Why is `-race` silent here?

  No happens-before violation to find. Every access to `w.stock` and
  `w.reserved` — the snapshot read in `snapshot()` (main.go:68–72) and the
  write-back in `releaseExpired` (main.go:85–94) — is taken under `w.mu`,
  same as every `Take` and `Reserve` call. The race detector's model is
  per-memory-word: it watches for two accesses to the same address, at
  least one a write, with no synchronization edge between them. Here every
  single access *is* synchronized; `mu.Lock`/`Unlock` gives the detector
  all the happens-before edges it needs, so it has nothing to report.

  What it can't see is that the lock is released and reacquired *across*
  the 300ms sleep (main.go:79–85), and that the value carried across that
  gap — the `stock` snapshot — goes stale while unlocked. That's not a
  synchronization bug, it's a logic bug: correct mutual exclusion around a
  stale read-modify-write. `-race` checks whether accesses are ordered; it
  doesn't and can't check whether the *values* you're carrying across an
  unlock are still true when you use them later. That's exactly the gap a
  watchpoint fills — it doesn't care about locks at all, it just tells you
  every time the memory changes, lock or no lock.

## Common pitfalls

- **Not choosing the target deliberately.** `watch -w store.stock[0]`
  on a fast mover will fire dozens of times before the janitor arrives.
  Lean on the exercise's cover story to justify item 5.
- **Setting the watchpoint too early.** Not an address problem — `store`
  is static data with a perfectly stable address. It's *symbol scope*:
  until you're stopped in a `main`-package frame, Delve can't resolve the
  expression at all (`unable to find function context` at entry,
  `could not find symbol store.stock` in `runtime.main`). Break on
  `main.main` first, then `watch`.
- **Only 4 watchpoints.** On amd64 and arm64 you get four hardware
  registers, and the 5th fails with a raw debugserver error rather than a
  friendly message: `protocol error E09 during set breakpoint for packet
  $Z2,1003c1868,8`. Don't be thrown by it. In practice you never want
  more than one or two.
- **Line reported = line after the write.** Expect `main.go:93` in the
  hit for a write at `main.go:92`. `list` (or the assembly with
  `disassemble -l`) at the reported line makes this obvious.
- **`-race` false confidence.** Locks make accesses atomic. Reasoning
  across time is a separate correctness question that no dynamic race
  detector can see.
- **Attach-mode.** If demoing under `dlv attach`, watchpoints still
  work — but *not* straight off the attach stop, which lands in runtime
  code. Set a breakpoint in a `main`-package function and `continue` into
  it first, then `watch`. On Linux you may also need
  `sysctl kernel.yama.ptrace_scope=0`.
