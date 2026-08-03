# Exercise 3: The Watchpoint Stakeout

**Time: ~25 minutes** | Difficulty: advanced

## Problem

A warehouse inventory service. Four picker goroutines fulfill a fixed,
deterministic order book; a janitor goroutine periodically returns stock
held by expired reservations. Every touch of the stock table goes through
a mutex.

```bash
go run -race .        # the race detector approves of this program
```

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

Stock in this system can only go **down** (orders take units off the
shelf; the janitor merely *returns* units a reservation already took).
Yet we ended up 30 units rich. Someone is writing bad values into
`store.stock`, while holding the lock, so `-race` is silent, and every
function involved looks reasonable in review.

## Your Task

Don't reason about the code. **Stake out the memory.** Use a hardware
watchpoint to catch the corrupting write mid-act, with the culprit
goroutine's stack in hand. Then fix it:

```
SUCCESS: books balance
```

## Choosing a Stakeout Target

You want a slot in `store.stock` that (a) ends up wrong and (b) has few
legitimate writes, so you aren't stopping every 5ms. Check the order book
in `buildOrders`: the sticker (item 5) is a slow mover, five orders in
the whole batch, yet it finishes at 99. Count its moves: 100 - 3 takes
- 2 reserves + 2 returns = 97. It shows 99. **Watch item 5.**

## Hints

<details>
<summary>Hint 1: setting the watchpoint</summary>

`store` is a package-level variable, so it's in scope the moment the
process stops anywhere in `main`:

```
dlv debug
(dlv) break main.main
(dlv) continue
(dlv) watch -w store.stock[5]
Watchpoint store.stock[5] set at 0x...
(dlv) continue
```

`-w` = stop on writes (there's also `-r` and `-rw`). It's a *hardware*
watchpoint: the CPU traps the write no matter which goroutine, which
function, or which pointer alias performs it. You get 4 of them
(amd64 and arm64 both).

</details>

<details>
<summary>Hint 2: reading the hits</summary>

Every stop tells you who wrote, from where. Expect this sequence:

1. one hit in `main.main` (the initial `stock[i] = 100` fill loop)
2. a handful of hits in `Take` / `Reserve`, the legitimate order flow,
   value stepping down 100 → 99 → 98 → 97 → 96 → 95
3. then a hit where the value **goes up**. That one. `stack` it.

Keep `print store.stock[5]` on your fingers between continues, or
`display -a store.stock[5]` to print it automatically at every stop.

</details>

<details>
<summary>Hint 3: why the janitor's write is wrong</summary>

You caught `releaseExpired` writing 99 over 95. It computed that 99 from
`stock[item] + reserved[item]`, but look at *which* `stock` that is: the
snapshot it took before its 300ms "ledger scan", not the live table. Any
sale that happened during those 300ms is erased by the write-back. It's a
stale read-modify-write: invisible to the race detector because both the
read and the write held the lock, they just didn't hold it *for the
duration of the reasoning*.

</details>

## Solution

<details>
<summary>Full walkthrough + fix</summary>

### Walkthrough (verified transcript)

Goroutine IDs — and the exact order the first few hits arrive in — vary run
to run. The shape is what's stable: a run of decrements, then one increment.

```
(dlv) break main.main
(dlv) continue
(dlv) watch -w store.stock[5]
Watchpoint store.stock[5] set at 0x...
(dlv) continue
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
99                                    <- WENT UP BY 4. caught.
(dlv) stack 3
0  0x... in main.(*Warehouse).releaseExpired
   at ./main.go:93
1  0x... in main.main.func1                       <- the janitor goroutine
   at ./main.go:160
2  0x... in runtime.goexit
   at /usr/local/go/src/runtime/asm_arm64.s:1447
```

The culprit line (the hit reports the line *after* the write):

```go
w.stock[item] = stock[item] + reserved[item]   // main.go:92
```

`stock` here is the **snapshot** captured before the 300ms expiry scan.
The janitor remembered `stock[5]` as 97 (with 2 reserved), slept through
two more sales (97 → 95), then wrote back 97 + 2 = 99, erasing both
sales. Same story, at higher volume, on items 4 and 0–3: +30 units of
phantom inventory.

### The fix

Make the write-back an *increment* of live state, not an overwrite from
stale state:

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

...and since the stock snapshot is now unused, the compiler will insist:

```go
_, reserved := w.snapshot()
```

(Note `reserved[item]`, the snapshot count of what to release, is still
fine to use: reservations only grow between snapshot and write-back, and
releasing only the snapshotted ones is correct. The bug was overwriting
`stock` with a stale *absolute* value.)

Deeper lesson: **snapshot + slow work + write-back of absolutes** is a
stale-RMW even when every individual access is locked. Locks make
accesses atomic; they don't make *reasoning across time* atomic. The
race detector checks happens-before on accesses, so it cannot see this,
but the CPU saw the write, and the watchpoint made it testify.

### Verify

```
go run -race .
...
SUCCESS: books balance
```

</details>

## Discussion Questions

- Watchpoints fire on *every* write, wanted or not. What made this
  program stake-out-friendly, and what would you do in a system where the
  hot item gets 1,000 writes/sec? (Conditions on the watchpoint, watching
  a rarely-written aggregate instead, or `-r` on a sentinel value...)
- The drift (+30) is timing-dependent, but the *bug* triggers every run.
  What structural property guarantees that? (The janitor's scan takes
  300ms while pickers need ~400ms for the batch, overlap is built in.)
- Could you have found this with the execution tracer? With logging?
  What's the minimal evidence that convicts the janitor, and which tool
  produces it fastest?
