package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Account holds a single customer's balance.
type Account struct {
	ID      int
	Owner   string
	balance int
}

// Bank manages a set of accounts. All exported methods are safe for
// concurrent use: every method acquires b.mu before touching shared state.
type Bank struct {
	mu       sync.RWMutex
	accounts map[int]*Account
	nextID   int
}

func NewBank() *Bank {
	return &Bank{
		accounts: make(map[int]*Account),
		nextID:   1,
	}
}

// CreateAccount opens a new account and returns its ID.
func (b *Bank) CreateAccount(owner string, initialBalance int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	b.accounts[id] = &Account{ID: id, Owner: owner, balance: initialBalance}
	return id
}

// GetBalance returns the current balance of an account.
func (b *Bank) GetBalance(id int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if account, ok := b.accounts[id]; ok {
		return account.balance
	}
	return 0
}

// Transfer moves money between two accounts. It fails (returns false) if
// the source account has insufficient funds.
//
// Performance note: transfers are our hottest path. Since a transfer never
// adds or removes accounts, a read lock on the map is sufficient and lets
// transfers run in parallel.
func (b *Bank) Transfer(fromID, toID, amount int) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	from, ok1 := b.accounts[fromID]
	to, ok2 := b.accounts[toID]
	if !ok1 || !ok2 || fromID == toID {
		return false
	}

	if from.balance < amount {
		return false
	}

	newFromBalance := from.balance - amount
	newToBalance := to.balance + amount

	// Compliance: large transfers require an additional fraud check
	// before the money moves.
	if amount > 95 && !fraudCheck(from, to, amount) {
		return false
	}

	from.balance = newFromBalance
	to.balance = newToBalance
	return true
}

// fraudCheck simulates the extra validation work required for large
// transfers. It always passes; what matters is that it takes time.
func fraudCheck(from, to *Account, amount int) bool {
	h := 0
	for i := range 800 {
		h = h*31 + from.ID + to.ID + amount + i
	}
	return h != 1 // the simulated check always passes
}

// TotalBalance sums all account balances. Money is only moved, never
// created or destroyed, so this should be constant for the lifetime of
// the bank.
func (b *Bank) TotalBalance() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	total := 0
	for _, account := range b.accounts {
		total += account.balance
	}
	return total
}

// LargestAccount returns the account with the highest balance, for the
// end-of-day VIP report.
func (b *Bank) LargestAccount() *Account {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var largest *Account
	for _, account := range b.accounts {
		if largest == nil || account.balance > largest.balance {
			largest = account
		}
	}
	return largest
}

// teller processes a stream of random transfers.
func teller(id int, bank *Bank, accountIDs []int, wg *sync.WaitGroup) {
	defer wg.Done()

	completed := 0
	for range 500 {
		from := accountIDs[rand.Intn(len(accountIDs))]
		to := accountIDs[rand.Intn(len(accountIDs))]
		amount := rand.Intn(100) + 1

		if bank.Transfer(from, to, amount) {
			completed++
		}

		// Simulate time between transactions.
		time.Sleep(time.Duration(rand.Intn(300)) * time.Microsecond)
	}

	fmt.Printf("Teller %d completed %d transfers\n", id, completed)
}

// vipReporter periodically reports the biggest account.
func vipReporter(bank *Bank, done chan struct{}) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			vip := bank.LargestAccount()
			if vip != nil {
				fmt.Printf("[VIP report] %s (account %d) leads with balance %d\n",
					vip.Owner, vip.ID, vip.balance)
			}
		case <-done:
			return
		}
	}
}

func main() {
	bank := NewBank()

	// Open the accounts before any concurrency starts.
	const numAccounts = 8
	const initialBalance = 1000
	owners := []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi"}

	accountIDs := make([]int, 0, numAccounts)
	for i := range numAccounts {
		accountIDs = append(accountIDs, bank.CreateAccount(owners[i], initialBalance))
	}

	expectedTotal := numAccounts * initialBalance
	fmt.Printf("Opened %d accounts, total on deposit: %d\n\n", numAccounts, expectedTotal)

	// Run the tellers.
	var wg sync.WaitGroup
	numTellers := 4
	for i := range numTellers {
		wg.Add(1)
		go teller(i, bank, accountIDs, &wg)
	}

	// Run the VIP reporter.
	done := make(chan struct{})
	go vipReporter(bank, done)

	wg.Wait()
	close(done)

	// The books must balance: transfers move money, they never mint it.
	finalTotal := bank.TotalBalance()
	fmt.Printf("\nExpected total: %d\n", expectedTotal)
	fmt.Printf("Actual total:   %d\n", finalTotal)
	if finalTotal == expectedTotal {
		fmt.Println("AUDIT PASSED: the books balance")
	} else {
		fmt.Printf("AUDIT FAILED: %d unaccounted for!\n", finalTotal-expectedTotal)
	}
}
