// Package cache is the one cache of the services: what a process keeps between calls, bounded in memory and in time.
// Every cache of the platform is a Cache: its entries have an estimated size and the cache holds at most Bounds.Bytes
// of them, an entry not read for Bounds.TTL goes, and the least recently read goes first when room is needed.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// EntryBytes is the estimated cost of an entry beyond its own size: the element of the list, the slot of the map, the
// times.
const EntryBytes = 96

// Bounds are the limits of a cache: Bytes the memory its entries hold, estimated (at least one byte), TTL how long an
// entry no one reads is kept (at least one nanosecond).
type Bounds struct {
	Bytes int64
	TTL   time.Duration
}

// Cache is a map bounded by Bounds, least recently read evicted first. Safe for concurrent use. A value is handed out
// as stored: a caller that changes what it reads copies it first.
type Cache[K comparable, V any] struct {
	bounds func() Bounds
	size   func(K, V) int64
	// Now is the clock (time.Now when nil; a test seam).
	Now func() time.Time

	mu    sync.Mutex
	ll    list.List // front: the most recently read
	m     map[K]*list.Element
	bytes int64
	stats Stats // the reads and the drops so far
}

type entry[K comparable, V any] struct {
	key  K
	val  V
	size int64
	at   time.Time // last read or write
}

// New returns a cache whose bounds are asked at each write (a configuration read late, Fixed for constant ones) and
// whose entries are size bytes each, estimated.
func New[K comparable, V any](bounds func() Bounds, size func(K, V) int64) *Cache[K, V] {
	return &Cache[K, V]{bounds: bounds, size: size, m: map[K]*list.Element{}}
}

// Fixed are constant bounds.
func Fixed(b Bounds) func() Bounds { return func() Bounds { return b } }

func (c *Cache[K, V]) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache[K, V]) limits() Bounds {
	b := c.bounds()
	return Bounds{Bytes: max(b.Bytes, 1), TTL: max(b.TTL, 1)}
}

// Get is the value of k, when kept and read within the TTL; reading it makes it the most recent.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.get(k, c.now(), c.limits().TTL)
	if ok {
		c.stats.Hits++
	} else {
		c.stats.Misses++
	}
	return v, ok
}

func (c *Cache[K, V]) get(k K, now time.Time, ttl time.Duration) (V, bool) {
	el, ok := c.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if now.Sub(e.at) >= ttl {
		c.remove(el)
		c.stats.Expired++
		var zero V
		return zero, false
	}
	e.at = now
	c.ll.MoveToFront(el)
	return e.val, true
}

// Put keeps v for k. A value larger than the whole cache is not kept (and drops what k held).
func (c *Cache[K, V]) Put(k K, v V) {
	c.Update(k, func(V, bool) (V, bool) { return v, true })
}

// Update changes the entry of k under the lock of the cache: fn gets the value kept (ok false when none, or expired)
// and returns the value to keep, keep false leaving the cache as it was. The size is estimated again.
func (c *Cache[K, V]) Update(k K, fn func(v V, ok bool) (V, bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now, b := c.now(), c.limits()
	old, ok := c.get(k, now, b.TTL)
	v, keep := fn(old, ok)
	if !keep {
		return
	}
	size := c.size(k, v) + EntryBytes
	if el, ok := c.m[k]; ok {
		c.remove(el)
	}
	if size > b.Bytes {
		c.stats.TooLarge++
		return
	}
	c.m[k] = c.ll.PushFront(&entry[K, V]{key: k, val: v, size: size, at: now})
	c.bytes += size
	c.evict(now, b)
}

// evict drops from the least recently read while the cache is over its bytes or the entry expired.
func (c *Cache[K, V]) evict(now time.Time, b Bounds) {
	for el := c.ll.Back(); el != nil; el = c.ll.Back() {
		e := el.Value.(*entry[K, V])
		if c.bytes <= b.Bytes && now.Sub(e.at) < b.TTL {
			return // the rest was read later
		}
		if c.bytes > b.Bytes {
			c.stats.Evicted++
		} else {
			c.stats.Expired++
		}
		c.remove(el)
	}
}

func (c *Cache[K, V]) remove(el *list.Element) {
	e := c.ll.Remove(el).(*entry[K, V])
	delete(c.m, e.key)
	c.bytes -= e.size
}

// Remove drops k.
func (c *Cache[K, V]) Remove(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		c.remove(el)
	}
}

// Clear drops every entry.
func (c *Cache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.m)
	c.bytes = 0
}

// Len is the number of entries kept (expired ones included until a write or a read reaches them).
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Bytes is the estimated memory the entries hold.
func (c *Cache[K, V]) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Stats are what a cache did and holds: Hits and Misses the reads (Get) that found a value or none, Evicted the entries
// dropped to make room, Expired those dropped unread for the TTL, TooLarge the values larger than the whole cache, never
// kept; Entries, Bytes and Limit what it holds now and may hold.
type Stats struct {
	Hits, Misses, Evicted, Expired, TooLarge int64
	Entries                                  int
	Bytes, Limit                             int64
}

// Stats are the counts of the cache so far and what it holds now.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries, s.Bytes, s.Limit = len(c.m), c.bytes, c.limits().Bytes
	return s
}
