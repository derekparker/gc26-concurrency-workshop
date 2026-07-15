# Exercise 3: The Bank That Passed Code Review (~15 min)

## The Situation

This banking system was written by someone who clearly knows about data
races: there's a `sync.RWMutex` in the `Bank`, **every** method locks it,
`go vet` is clean, and the code reads like textbook Go. Four tellers move
money between eight accounts; since transfers only *move* money, the total
on deposit must stay exactly 8000 forever. An audit checks this at exit.

```bash
go run .
```

```
Opened 8 accounts, total on deposit: 8000

[VIP report] frank (account 6) leads with balance 1290
Teller 2 completed 448 transfers
...
Expected total: 8000
Actual total:   8000
AUDIT PASSED: the books balance
```

Now run it in a loop:

```bash
for i in $(seq 20); do go run . 2>&1 | grep AUDIT; done
```

```
AUDIT PASSED: the books balance
AUDIT PASSED: the books balance
AUDIT FAILED: -44 unaccounted for!
AUDIT PASSED: the books balance
...
```

Money vanishes, or appears, roughly one run in five or ten. In production
this is the bug report that says *"the ledger drifts by a few cents a day,"*
and staring at the code doesn't help, because every method visibly takes a
lock.

## Your Task

1. Diagnose with the detector:

   ```bash
   go run -race .
   ```

2. From the reports, answer precisely:
   - Which lines race with which lines?
   - **Both goroutines hold the mutex when they race.** How is that
     possible?
   - There is a second, unrelated race involving the VIP reporter. Find it.
3. Fix the system:
   - `go run -race .` → no warnings.
   - The audit must pass on every run (loop it 20×).
   - Keep `Transfer` safe from deadlock, think about what happens if you
     move to finer-grained locking later.

## Questions to Discuss

- Why didn't `go vet` catch this? What *can* static analysis know here?
- Why does the corruption cluster around large transfer amounts? (Read
  `Transfer` carefully, when is the window between *deciding* and
  *committing* widest?)
- After your fix: is `Transfer` now *atomic* from an observer's point of
  view, or merely race-free? What could `GetBalance` observe mid-transfer
  before your fix, even on lucky runs?

<details>
<summary><strong>Hint 1</strong> (the mutex is real, the protection isn't)</summary>

Look at *which kind* of lock each method takes. `Transfer` takes
`RLock`, the comment even justifies it: the map isn't being modified, so a
read lock "is sufficient." The map access is indeed safe... but then it
**writes** `from.balance` and `to.balance`. An `RWMutex` allows any number
of `RLock` holders at once, so N tellers mutate balances *simultaneously*,
all "holding the lock." A read lock makes concurrent writers safe from
`CreateAccount`, not from each other.

The race report shows it: read at `main.go:70` (`from.balance < amount`) vs
write at `main.go:84` (`to.balance = newToBalance`), both in `Transfer`,
both under `RLock`.

</details>

<details>
<summary><strong>Hint 2</strong> (the second race)</summary>

`LargestAccount` returns an `*Account`, a pointer into `Bank`'s protected
state. The lock is released when it returns, but `vipReporter` then reads
`vip.balance` with no lock at all, racing with the tellers. Returning
pointers (or maps/slices) to lock-protected data leaks it out of the
critical section. Return a *copy* instead.

Also notice `fraudCheck`: the balances are read at lines 74–75, but written
at 83–84, with a slow compliance check between them for amounts > 95.
That's why big transfers corrupt more often: the read-decide-commit window
is widest there. A race on a rarely-slow path = a bug that only shows under
"weird" traffic.

</details>

<details>
<summary><strong>Solution</strong></summary>

Minimal correct fix, `Transfer` mutates, so it takes the **write** lock,
and `LargestAccount` returns a copy:

```go
func (b *Bank) Transfer(fromID, toID, amount int) bool {
	b.mu.Lock()         // was RLock: we write balances
	defer b.mu.Unlock()
	// ... body unchanged ...
}

func (b *Bank) LargestAccount() Account { // value, not *Account
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
	return *largest     // copy made while the lock is held
}
```

(`vipReporter` then uses the returned value; check for a zero `Account`
instead of `nil`.)

`GetBalance` / `TotalBalance` may keep `RLock`, once all writes happen
under the exclusive lock, read-locked reads are properly ordered.

**Why this beats "clever" fixes:**

- *Atomic balances* (`atomic.Int64`) would silence the detector but break
  the *invariant*: the insufficient-funds check and the two balance updates
  must be one atomic decision, or two goroutines can both pass the check and
  overdraw. Race-free ≠ correct, this is a check-then-act (logical) race
  the detector cannot see.
- *Per-account mutexes* scale better but introduce lock-ordering: locking
  `from` then `to` while another teller locks `to` then `from` deadlocks.
  The standard trick is to always lock the lower account ID first. Great
  extension if you finish early, section 03 shows how to debug the deadlock
  you'll write on the first attempt.

**Takeaways:**

1. `RLock` means "I promise I only read." The compiler doesn't check that
   promise, the race detector does.
2. Never let pointers to protected data escape the critical section.
3. The window between reading state and writing a decision is where races
   live; the wider (slower) the window, the more corruption, but the race
   exists at any width, and `-race` finds it at any width.

</details>
