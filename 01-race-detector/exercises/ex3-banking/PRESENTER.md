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
[VIP report] carol (account 2) leads with balance 1355
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
   first `Transfer`-vs-`Transfer` report aloud with the students (report
   order is nondeterministic and the line numbers vary — any of 70/74/75
   against 83/84):

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

The whole fix is in `solution.diff`, applied from this directory:

```bash
git apply solution.diff        # Transfer takes Lock, LargestAccount returns a copy
```

> Undo it with `git apply -R solution.diff` when you want the racy version
> back for the next session.

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

Answers are for you, not the slides. Let students swing first — the wrong
answers are the teachable part.

- Why didn't `go vet` catch this? What *can* static analysis know about a
  mutex, and what can it never know?

  `go vet` is purely syntactic and type-level. It can catch things like
  `sync.Mutex` (or a struct embedding one) being copied by value — the
  `copylocks` check — because that's a static property of the code: a
  `Mutex` type appearing on the right-hand side of an assignment or passed
  by value. What it fundamentally cannot know is *intent*: which mutex, if
  any, is supposed to protect which field, whether a given access needs a
  read lock or a write lock, or whether a lock is even held at the point of
  an access. `go vet` has no model of a "critical section" — it never
  simulates two goroutines running at once, so it has no way to notice that
  `Transfer` writes `from.balance` while holding only `RLock`. That's not a
  gap in this particular check; it's out of scope for what static analysis
  over a single control-flow path can express. `-race` catches it because
  it's a dynamic tool — it watches actual concurrent executions and their
  happens-before edges, which is the only place "was this access properly
  synchronized *relative to that other access*" is even a well-formed
  question.

- Both goroutines hold `b.mu` when they race. If the lock is held, how is
  that a race? (Force them to say the words: `RWMutex.RLock` does not
  exclude other `RLock` holders.)

  Because "holding the lock" is doing less work than it sounds like.
  `RWMutex` has two admission policies: `Lock` (the writer lock) excludes
  *everyone* — other writers and all readers. `RLock` only excludes
  writers; it explicitly allows any number of concurrent `RLock` holders.
  The author's mental model was "the lock is held, so we're safe" — but
  the actual guarantee `RLock` gives you is "no writer can run while I
  hold this," not "no one else can run." Four tellers can all call
  `Transfer`, all call `b.mu.RLock()`, all get in, and all run
  `from.balance = newFromBalance` at the same time — every one of them
  technically "holding the lock," and none of them excluded from anything
  that matters. The bug isn't that the mutex failed; it's that `RLock` was
  the wrong admission policy for a method that mutates. The one-sentence
  version to land: `RWMutex.RLock` does not exclude other `RLock` holders,
  it only excludes `Lock`.

- Corruption clusters around large transfer amounts. Why? What does that
  say about the relationship between a race's *window* and its *symptoms*?

  `fraudCheck` only runs for `amount > 95`, and it sits *between* the
  balance reads (`from.balance < amount` at line 70, and the snapshots at
  74–75) and the balance writes (83–84). For small transfers that gap is a
  handful of instructions; for large transfers it's ~800 loop iterations of
  compute. A race is a collision between two goroutines' accesses to the
  same memory — the wider the window during which another goroutine can
  interleave, the higher the probability two tellers land inside it on any
  given run. So the *symptom rate* — how often you observe corruption — is
  governed by the window size, but the *existence* of the race is not. The
  race is present on every transfer, small or large, the moment `Transfer`
  takes `RLock` instead of `Lock`; a $1 transfer with no `fraudCheck` delay
  is just as racy, it's only less likely to get caught in the act. This is
  the same point as ex1's "the race is on 100% of runs, the loss is rare" —
  worth calling back to if you taught that exercise first. It's also why
  "it passed in testing" is such a weak signal: your test's window sizes
  and thread interleavings are not your production load's.

- After the fix, is `Transfer` *atomic* to an outside observer, or merely
  race-free? What could `GetBalance` observe mid-transfer if we replaced
  the mutex with per-field atomics?

  With the mutex fix, `Transfer` is genuinely atomic to an outside
  observer, not just race-free. Because the entire read-decide-commit
  sequence (check funds, run `fraudCheck`, write both balances) happens
  under one held `Lock`, and `GetBalance`/`TotalBalance` can only run under
  `RLock` — which `Lock` excludes — no outside reader can ever observe the
  state between `from.balance` being debited and `to.balance` being
  credited. From `GetBalance`'s point of view, a transfer either hasn't
  happened yet or has fully happened; there is no in-between. That's a
  stronger property than "no data race" — it's a whole-operation
  invariant, and it's exactly what the Common pitfalls section's
  `atomic.Int64` warning is about from the other direction: swapping the
  mutex for per-field atomics on `balance` would make each individual
  write race-free (each `Store` is atomic on its own) but would destroy
  this atomicity. `GetBalance(fromID)` could run *after* the debit's
  atomic store but `GetBalance(toID)` could run *before* the credit's
  atomic store — a window where the money has left `from` and not yet
  landed in `to`. No single field is ever corrupted or torn, and `-race`
  would report nothing, but an external observer polling both balances
  could see a snapshot where the total is short by exactly `amount` — money
  transiently vanishes from the ledger's point of view even though every
  individual memory access was perfectly synchronized. That's the
  distinction to land: atomics buy you per-access safety; a mutex around
  the whole operation buys you a consistency invariant across accesses.
  Race-free is necessary, not sufficient, for atomic-to-an-observer.

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
  reports remain. Exactly two: `Transfer`'s writes at `main.go:83`/`84`
  racing `vipReporter`'s read at `main.go:159` — through the pointer
  `LargestAccount` handed out. Note that `LargestAccount:120` itself is
  now clean; the leak, not the lock, is what's left. The race is a
  pointer leak, not a lock issue.
- **Changing `LargestAccount` to hold the lock across `vipReporter`'s
  usage.** Won't work — the goroutines are separate. Only a *value copy*
  removes the shared state.
- **Trying `sync.RWMutex.Upgrade`.** No such thing in `sync`. Educational
  moment: promoting an `RLock` to a `Lock` is a known deadlock pattern in
  most `RWMutex` implementations; Go's just doesn't offer it.
