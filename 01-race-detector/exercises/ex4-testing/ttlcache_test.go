package ttlcache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestSetGet covers the basics: what goes in comes out, and unknown keys
// miss.
func TestSetGet(t *testing.T) {
	c := New(time.Minute, time.Minute)
	defer c.Close()

	c.Set("greeting", "hello")

	if v, ok := c.Get("greeting"); !ok || v != "hello" {
		t.Fatalf(`Get("greeting") = %q, %v; want "hello", true`, v, ok)
	}
	if _, ok := c.Get("nope"); ok {
		t.Fatal(`Get("nope") returned ok for a key that was never set`)
	}

	hits, misses, _ := c.Stats()
	if hits != 1 || misses != 1 {
		t.Fatalf("Stats() = %d hits, %d misses; want 1, 1", hits, misses)
	}
}

// TestConcurrentAccess hammers the cache from several goroutines at once,
// the way a real service would. Readers verify they always see a value that
// was actually stored under the key.
func TestConcurrentAccess(t *testing.T) {
	c := New(time.Minute, time.Minute)
	defer c.Close()

	const keys = 10
	for i := range keys {
		c.Set(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
	}

	var wg sync.WaitGroup

	// Four readers hammer the pre-populated keys.
	for range 4 {
		wg.Go(func() {
			for i := range 1000 {
				want := fmt.Sprintf("value-%d", i%keys)
				v, ok := c.Get(fmt.Sprintf("key-%d", i%keys))
				if !ok {
					t.Errorf("key-%d disappeared", i%keys)
				} else if v != want {
					t.Errorf("Get(key-%d) = %q, want %q", i%keys, v, want)
				}
			}
		})
	}

	// Two writers refresh the same entries concurrently.
	for range 2 {
		wg.Go(func() {
			for i := range 500 {
				c.Set(fmt.Sprintf("key-%d", i%keys), fmt.Sprintf("value-%d", i%keys))
			}
		})
	}

	wg.Wait()

	if got := c.Len(); got != keys {
		t.Errorf("Len() = %d, want %d", got, keys)
	}
	if hits, _, _ := c.Stats(); hits == 0 {
		t.Error("expected at least one cache hit")
	}
}

// TestExpiryAndSweep_RealClock verifies TTL expiry and the janitor sweep
// against the real clock. Every sleep needs slack on top of the nominal
// duration because CI machines stall — and the test still takes multiple
// wall-clock seconds every single run.
func TestExpiryAndSweep_RealClock(t *testing.T) {
	c := New(1*time.Second, 2*time.Second) // ttl=1s, janitor sweeps every 2s
	defer c.Close()

	c.Set("greeting", "hello")

	if _, ok := c.Get("greeting"); !ok {
		t.Fatal("entry should be live immediately after Set")
	}

	// Wait "long enough" for the 1s TTL to pass. 200ms of slack, in case
	// the scheduler is slow. (How much slack is enough? Nobody knows.)
	time.Sleep(1200 * time.Millisecond)

	if _, ok := c.Get("greeting"); ok {
		t.Fatal("entry should have expired")
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1 (expired but not swept yet)", got)
	}

	// Wait "long enough" for the janitor's 2s tick to fire.
	time.Sleep(1200 * time.Millisecond)

	if got := c.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0 (janitor should have swept)", got)
	}
	if _, _, swept := c.Stats(); swept != 1 {
		t.Fatalf("Stats() swept = %d, want 1", swept)
	}
}

// TestExpiryAndSweep_Synctest is YOUR task: the same behavior as
// TestExpiryAndSweep_RealClock, verified inside a testing/synctest bubble —
// instant, deterministic, and exact to the millisecond. See the README.
func TestExpiryAndSweep_Synctest(t *testing.T) {
	t.Skip("TODO: rewrite TestExpiryAndSweep_RealClock using synctest.Test")
}
