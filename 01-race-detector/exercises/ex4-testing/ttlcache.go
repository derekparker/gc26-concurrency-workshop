// Package ttlcache implements a small in-memory cache where every entry
// expires after a fixed TTL. A background janitor goroutine sweeps expired
// entries periodically so the map doesn't grow forever.
package ttlcache

import (
	"sync"
	"time"
)

type entry struct {
	value     string
	expiresAt time.Time
}

// Cache is a fixed-TTL string cache safe for concurrent use.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]entry
	ttl     time.Duration

	// Stats: hits/misses are recorded on the Get path, swept counts
	// entries removed by the janitor.
	hits   int
	misses int
	swept  int

	done chan struct{}
}

// New creates a cache whose entries expire ttl after they are set, and
// starts a janitor goroutine that removes expired entries every sweepEvery.
// Call Close to stop the janitor.
func New(ttl, sweepEvery time.Duration) *Cache {
	c := &Cache{
		entries: make(map[string]entry),
		ttl:     ttl,
		done:    make(chan struct{}),
	}
	go c.janitor(sweepEvery)
	return c
}

// Set stores value under key. The entry expires ttl from now.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{value: value, expiresAt: time.Now().Add(c.ttl)}
}

// Get returns the value for key, if present and not expired. Expired
// entries count as misses even before the janitor sweeps them.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		c.misses++ // stats only, and we hold the lock, so this is safe
		return "", false
	}
	c.hits++
	return e.value, true
}

// Len reports how many entries are in the map, including entries that have
// expired but have not been swept yet.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Stats reports how many Gets hit and missed, and how many expired entries
// the janitor has removed.
func (c *Cache) Stats() (hits, misses, swept int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, c.swept
}

// Close stops the janitor goroutine.
func (c *Cache) Close() {
	close(c.done)
}

func (c *Cache) janitor(sweepEvery time.Duration) {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.sweep()
		case <-c.done:
			return
		}
	}
}

func (c *Cache) sweep() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, key)
			c.swept++
		}
	}
}
