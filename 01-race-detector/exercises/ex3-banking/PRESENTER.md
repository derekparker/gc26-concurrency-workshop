# Presenter: Exercise 3 — The Bank That Passed Code Review

> Presenter-only. Students use README.md.

## Goal

The lock is real. `go vet` is clean. Every method visibly acquires the
mutex. And money still vanishes about one run in five. Teach students that
**`RLock` is a promise you make to the compiler that only `-race`
verifies** — and that leaking pointers out of a critical section is a race
even when everything else is locked correctly.

## Reproduce

From `01-race-detector/exercises/ex3-banking`:

```bash
go run .
```

Usually clean:

```
Opened 8 accounts, total on deposit: 8000

[VIP report] frank (account 6) leads with balance 1290
Teller 2 completed 448 transfers
...
Expected total: 8000
Actual total:   8000
AUDIT PASSED: the books balance
```

Loop it to see the drift:

```bash
for i in $(seq 20); do go run . 2>&1 | grep AUDIT; done
```

Observed on this machine (20 runs):

```
AUDIT PASSED: the books balance
AUDIT PASSED: the books balance
AUDIT PASSED: the books balance
AUDIT PASSED: the books balance
AUDIT FAILED: -152 unaccounted for!
AUDIT PASSED: the books balance
...
AUDIT FAILED: 11 unaccounted for!
AUDIT PASSED: the books balance
```

Roughly 1–3 failures per 20 runs, and the delta can be positive *or*
negative — money can appear or vanish. Now the detector, every run:

```bash
go run -race .
```

Expected (abridged):

```
WARNING: DATA RACE
Read at 0x00c0000ae098 by goroutine 8:
  main.(*Bank).Transfer()
      .../ex3-banking/main.go:70
Previous write at 0x00c0000ae098 by goroutine 10:
  main.(*Bank).Transfer()
      .../ex3-banking/main.go:84
...
Actual total:   11236
AUDIT FAILED: 3236 unaccounted for!
Found 15 data race(s)
exit status 66
```

## Root cause

Two independent bugs, both invisible to `go vet`:

1. **`Transfer` takes `RLock`.** The author's comment even justifies it:
   the *map* isn't being modified, so a read lock "is sufficient." True
   for the map — dangerously wrong for the balances it points into.
   `RWMutex` admits any number of `RLock` holders simultaneously, so all
   four tellers execute `from.balance = newFromBalance` /
   `to.balance = newToBalance` at the same time, each "holding the lock."
   `RLock` orders you against *writers* — nothing about it orders you
   against other readers who cheat.

   The race report shows it: read at `main.go:70`
   (`from.balance < amount`) vs write at `main.go:84`
   (`to.balance = newToBalance`), both inside `Transfer`, both under
   `RLock`.

2. **`LargestAccount` returns `*Account` — a pointer *into* protected
   state.** The lock is released when the function returns; `vipReporter`
   then reads `vip.balance` at `main.go:159` with **no lock at all**,
   racing tellers who are writing that balance. Leaking pointers to
   protected data leaks the data too.

Bonus talking point: `fraudCheck` runs *between* the read (lines 70/74–75)
and the write (lines 83–84), only for `amount > 95`. That widens the
read-decide-commit window for large transfers — which is why the corruption
clusters around big amounts. The race exists at every width; it just
manifests louder when the window is wide.

## Walkthrough

1. **"It works" — until it doesn't (3 min).** Run `go run .` once,
   `AUDIT PASSED`. Ship it? Loop 20 times with the one-liner above and
   count failures. The book drifts.

2. **`-race` on the guilty code (5 min).** `go run -race .`. Read the
   *first* report aloud with the students:

   ```
   Read at 0x00c0... by goroutine 8:
     main.(*Bank).Transfer() main.go:70
   Previous write at 0x00c0... by goroutine 10:
     main.(*Bank).Transfer() main.go:84
   ```

   Both stacks are inside `Transfer`. Same struct field. **Both under
   `RLock`.** Ask: *"How is this possible? The mutex is right there."*
   Answer, in one sentence: **`RWMutex.RLock` doesn't exclude other
   `RLock` holders.** The author wrote `RLock` because "we're just
   reading" — and then the code does `from.balance = ...`. The compiler
   never checks that promise.

3. **The second bug (3 min).** Skim the report list for
   `main.(*Bank).LargestAccount()` at `main.go:120` — that's the loop
   reading `account.balance` inside `LargestAccount`, racing writers.
   Then look at line 159 in `vipReporter` — `vip.balance` read outside
   any lock. `LargestAccount` returns a `*Account` — the pointer walks
   right out of the critical section.

4. **Why the check-then-act pattern is the deep bug (2 min).** Even after
   we fix `RLock`→`Lock`, `Transfer` still has a check (`from.balance <
   amount`), a slow step (`fraudCheck` for big amounts), and a commit
   (two balance writes). If you tried the "clever" fix of atomic
   balances, two goroutines could each pass the check and both overdraw —
   race-free, still broken. The lock isn't just protecting fields; it's
   making the whole read-decide-commit an atomic decision.

## Fix

Two small edits, no restructuring. `Transfer` mutates → it takes the
**write** lock. `LargestAccount` returns a **copy**.

```go
func (b *Bank) Transfer(fromID, toID, amount int) bool {
	b.mu.Lock()          // was RLock — we WRITE balances
	defer b.mu.Unlock()
	// ... body unchanged ...
}

func (b *Bank) LargestAccount() Account {   // value, not *Account
	b.mu.RLock()
	defer b.mu.RUnlock()

	var largest *Account
	for _, account := range b.accounts {
		if largest == nil || account.balance > largest.balance {
			largest = account
		}
	}
	if largest == nil {
		return Account{}
	}
	return *largest                        // copy made while lock is held
}
```

Update `vipReporter` to check for a zero `Account` instead of `nil`:

```go
vip := bank.LargestAccount()
if vip.ID != 0 {
    fmt.Printf("[VIP report] %s (account %d) leads with balance %d\n",
        vip.Owner, vip.ID, vip.balance)
}
```

`GetBalance` and `TotalBalance` can keep `RLock` — once all *writes*
happen under the exclusive lock, `RLock` readers see a consistent
snapshot.

Verify:

```bash
go run -race .
for i in $(seq 20); do go run . 2>&1 | grep AUDIT; done
```

Clean output:

```
AUDIT PASSED: the books balance
AUDIT PASSED: the books balance
... (20/20 pass, no drift) ...
```

`go run -race .`: no warnings, exit 0.

## Ask the room

- Why didn't `go vet` catch this? What *can* static analysis know about a
  mutex, and what can it never know?
- Both goroutines hold `b.mu` when they race. If the lock is held, how is
  that a race? (Force them to say the words: `RWMutex.RLock` does not
  exclude other `RLock` holders.)
- Corruption clusters around large transfer amounts. Why? What does that
  say about the relationship between a race's *window* and its *symptoms*?
- After the fix, is `Transfer` *atomic* to an outside observer, or merely
  race-free? What could `GetBalance` observe mid-transfer if we replaced
  the mutex with per-field atomics?

## Common pitfalls

- **"Just use `atomic.Int64` for `balance`."** Silences the detector,
  breaks the invariant. The insufficient-funds check and the two balance
  writes must be one atomic decision — two tellers can both pass the check
  and both overdraw. **Race-free ≠ correct.** This is a check-then-act
  race the detector cannot see.
- **Per-account mutexes on the first attempt.** Great instinct, subtle
  execution — locking `from` then `to` while another teller locks `to`
  then `from` deadlocks. Standard trick: always lock the lower `ID` first.
  Save it for finish-early students; section 03 will debug deadlocks.
- **Forgetting `LargestAccount`.** Students fix `RLock`→`Lock` on
  `Transfer`, rerun `-race`, and see a *smaller* pile of reports — but
  reports remain. They come from `LargestAccount` /`vipReporter`. The
  race is a pointer leak, not a lock issue.
- **Changing `LargestAccount` to hold the lock across `vipReporter`'s
  usage.** Won't work — the goroutines are separate. Only a *value copy*
  removes the shared state.
- **Trying `sync.RWMutex.Upgrade`.** No such thing in `sync`. Educational
  moment: promoting an `RLock` to a `Lock` is a known deadlock pattern in
  most `RWMutex` implementations; Go's just doesn't offer it.
