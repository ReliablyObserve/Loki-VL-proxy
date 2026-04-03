package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

type entry struct {
	value     []byte
	expiresAt time.Time
	sizeBytes int
}

// Cache is an in-memory TTL cache with per-key TTL override, max entries, and max bytes.
type Cache struct {
	mu         sync.RWMutex
	entries    map[string]entry
	defaultTTL time.Duration
	maxEntries int
	maxBytes   int
	curBytes   int

	// Stats
	Hits   atomic.Int64
	Misses atomic.Int64
	Evictions atomic.Int64
}

func New(ttl time.Duration, maxEntries int) *Cache {
	return NewWithMaxBytes(ttl, maxEntries, 256*1024*1024) // 256MB default
}

func NewWithMaxBytes(ttl time.Duration, maxEntries, maxBytes int) *Cache {
	c := &Cache{
		entries:    make(map[string]entry),
		defaultTTL: ttl,
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	}
	go c.cleanup()
	return c
}

func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		c.Misses.Add(1)
		return nil, false
	}
	c.Hits.Add(1)
	return e.value, true
}

// Set stores a value with the default TTL.
func (c *Cache) Set(key string, value []byte) {
	c.SetWithTTL(key, value, c.defaultTTL)
}

// SetWithTTL stores a value with a specific TTL.
func (c *Cache) SetWithTTL(key string, value []byte, ttl time.Duration) {
	size := len(value)
	// Don't cache entries larger than 10% of max bytes
	if size > c.maxBytes/10 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// If replacing existing entry, subtract old size
	if old, ok := c.entries[key]; ok {
		c.curBytes -= old.sizeBytes
	}

	c.evictIfNeeded(size)

	c.entries[key] = entry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
		sizeBytes: size,
	}
	c.curBytes += size
}

// Invalidate removes a specific key.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.curBytes -= e.sizeBytes
		delete(c.entries, key)
	}
}

// InvalidatePrefix removes all keys with the given prefix.
func (c *Cache) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			c.curBytes -= e.sizeBytes
			delete(c.entries, k)
		}
	}
}

// Size returns current number of entries and bytes.
func (c *Cache) Size() (entries int, bytes int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries), c.curBytes
}

func (c *Cache) evictIfNeeded(incomingSize int) {
	now := time.Now()
	// First pass: remove expired
	if len(c.entries) >= c.maxEntries || c.curBytes+incomingSize > c.maxBytes {
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				c.curBytes -= v.sizeBytes
				delete(c.entries, k)
				c.Evictions.Add(1)
			}
		}
	}
	// Second pass: evict ~10% if still over limits
	if len(c.entries) >= c.maxEntries || c.curBytes+incomingSize > c.maxBytes {
		target := c.maxEntries / 10
		if target < 1 {
			target = 1
		}
		count := 0
		for k, v := range c.entries {
			c.curBytes -= v.sizeBytes
			delete(c.entries, k)
			c.Evictions.Add(1)
			count++
			if count >= target && len(c.entries) < c.maxEntries && c.curBytes+incomingSize <= c.maxBytes {
				break
			}
		}
	}
}

func (c *Cache) cleanup() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				c.curBytes -= v.sizeBytes
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
