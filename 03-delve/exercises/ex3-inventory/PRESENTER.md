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
  gets 1,000 writes/sec? (Conditional watchpoints, watching a rarely-
  written aggregate, `-r` on a sentinel, sampling by goroutine.)
- The drift (+30) is timing-dependent, but the bug triggers every run.
  What structural property guarantees that? (Janitor scan = 300ms,
  pickers ~400ms/batch — overlap is built in.)
- Could you have found this with the execution tracer? With logging?
  What's the *minimal* evidence that convicts the janitor, and which
  tool produces it fastest?
- Why is `-race` silent here? (No happens-before violation. Both the
  read (snapshot) and the write held the lock. They just didn't hold it
  *across* the sleep.)

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
