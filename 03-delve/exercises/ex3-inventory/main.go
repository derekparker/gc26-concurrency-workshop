// Warehouse inventory service.
//
// Four pickers fulfill a fixed batch of orders, decrementing stock counts.
// A janitor goroutine periodically returns stock held by expired
// reservations. Every access to the stock table is protected by a mutex --
// run it with -race yourself: the race detector is perfectly happy.
//
// SYMPTOM: at the end of the run the books don't balance. Stock can only
// ever go DOWN (orders take units; the janitor merely returns units that a
// reservation already took), and yet the shelves end up with MORE stock
// than the order book allows. The drift changes from run to run. Somebody
// is writing a bad value into the stock table, and code review hasn't
// found it.
//
// Catch the culprit red-handed with a watchpoint.
package main

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const numItems = 6

var itemNames = [numItems]string{
	"gopher-plush", "gopher-mug", "gopher-tee",
	"gopher-cap", "gopher-pin", "gopher-sticker",
}

// Order is one line of the order book.
type Order struct {
	Item    int
	Reserve bool // held for payment confirmation instead of shipped now
}

// Warehouse tracks stock levels for each item. All methods are guarded by
// mu; the race detector reports nothing for this program.
type Warehouse struct {
	mu       sync.Mutex
	stock    [numItems]int64
	reserved [numItems]int64
}

// store is the single warehouse instance, allocated before main starts.
var store = &Warehouse{}

// Take removes n units of item from stock to fulfill an order.
func (w *Warehouse) Take(item int, n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stock[item] -= n
}

// Reserve takes n units of item off the shelf and holds them for a
// pending payment. If the payment never confirms, the janitor returns
// the units to the shelf.
func (w *Warehouse) Reserve(item int, n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stock[item] -= n
	w.reserved[item] += n
}

// snapshot returns a consistent copy of the warehouse state.
func (w *Warehouse) snapshot() (stock, reserved [numItems]int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stock, w.reserved
}

// releaseExpired returns expired reservations to the shelves.
//
// It works from a snapshot so that the (potentially slow) expiry scan
// does not hold the warehouse lock while it runs.
func (w *Warehouse) releaseExpired() {
	stock, reserved := w.snapshot()

	// Simulate a slow scan of the reservation ledger (database lookups,
	// expiry timestamp checks, ...).
	time.Sleep(300 * time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()
	for item := range numItems {
		if reserved[item] == 0 {
			continue
		}
		// Return the held units to the shelf and clear the reservation.
		w.stock[item] = stock[item] + reserved[item]
		w.reserved[item] -= reserved[item]
	}
}

// Total returns the total number of units on the shelves.
func (w *Warehouse) Total() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	var total int64
	for _, n := range w.stock {
		total += n
	}
	return total
}

// buildOrders produces the deterministic order book: every run fulfills
// exactly the same orders in the same submission sequence.
func buildOrders(count int) []Order {
	orders := make([]Order, count)
	for k := range count {
		orders[k] = Order{
			Item:    k % 5, // the fast movers
			Reserve: k%10 == 9,
		}
	}
	// The sticker is a slow mover: a handful of orders for item 5,
	// sprinkled through the batch.
	orders[15] = Order{Item: 5}
	orders[59] = Order{Item: 5, Reserve: true}
	orders[119] = Order{Item: 5, Reserve: true}
	orders[260] = Order{Item: 5}
	orders[280] = Order{Item: 5}
	return orders
}

func main() {
	log.SetFlags(log.Lmicroseconds)

	const (
		initialPerItem = 100
		numPickers     = 4
		numOrders      = 320
	)

	for i := range numItems {
		store.stock[i] = initialPerItem
	}
	initial := store.Total()

	book := buildOrders(numOrders)
	orders := make(chan Order, numOrders)
	for _, o := range book {
		orders <- o
	}
	close(orders)

	// Janitor: periodically return expired reservations to the shelves.
	stopJanitor := make(chan struct{})
	var janitorWG sync.WaitGroup
	janitorWG.Add(1)
	go func() {
		defer janitorWG.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				store.releaseExpired()
			case <-stopJanitor:
				return
			}
		}
	}()

	// Pickers: fulfill the order book.
	var wg sync.WaitGroup
	for range numPickers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range orders {
				if o.Reserve {
					store.Reserve(o.Item, 1)
				} else {
					store.Take(o.Item, 1)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Let the janitor return any remaining reservations, then stop it.
	time.Sleep(900 * time.Millisecond)
	close(stopJanitor)
	janitorWG.Wait()

	// Reconcile the books. Reservations were all returned, so:
	// expected = initial - (number of non-reserved orders).
	var sold int64
	for _, o := range book {
		if !o.Reserve {
			sold++
		}
	}
	final := store.Total()

	fmt.Println()
	log.Printf("initial stock: %d units", initial)
	log.Printf("orders shipped: %d units", sold)
	log.Printf("expected on shelves: %d units", initial-sold)
	log.Printf("actual on shelves:   %d units", final)
	for i := range numItems {
		log.Printf("  [%d] %-15s stock=%3d reserved=%d", i, itemNames[i], store.stock[i], store.reserved[i])
	}
	if drift := final - (initial - sold); drift != 0 {
		log.Printf("FAILED: inventory drift of %+d units", drift)
		os.Exit(1)
	}
	log.Println("SUCCESS: books balance")
}
