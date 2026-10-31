package cache

import (
	"testing"
	"time"
)

// The cache holds at most its bytes, the least recently read going first; an entry larger than the cache is not kept;
// an entry not read for the TTL goes, reading it keeps it.
func TestBounds(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New(Fixed(Bounds{Bytes: 1000, TTL: time.Minute}), func(_ string, v int64) int64 { return v - EntryBytes })
	c.Now = func() time.Time { return now }
	c.Put("a", 400)
	c.Put("b", 400)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a fits")
	}
	// a third passes the bound: b, read before a, goes
	c.Put("c", 400)
	if _, ok := c.Get("b"); ok {
		t.Fatal("the least recently read entry should have gone")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a was read last")
	}
	if c.Bytes() != 800 || c.Len() != 2 {
		t.Fatalf("held %d bytes in %d entries, want 800 in 2", c.Bytes(), c.Len())
	}
	// larger than the cache: not kept, and nothing else pushed out
	c.Put("big", 2000)
	if _, ok := c.Get("big"); ok || c.Len() != 2 {
		t.Fatal("an entry larger than the cache was kept")
	}
	// a replaced entry counts its own size
	c.Put("a", 100)
	if v, _ := c.Get("a"); v != 100 || c.Bytes() != 500 {
		t.Fatalf("replaced: %d, %d bytes", v, c.Bytes())
	}
	// read within the TTL an entry stays, not read it goes
	now = now.Add(40 * time.Second)
	c.Get("a")
	now = now.Add(40 * time.Second)
	if _, ok := c.Get("c"); ok {
		t.Fatal("an entry not read for the TTL is still served")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("an entry read within the TTL went")
	}
	// a write drops what expired
	now = now.Add(2 * time.Minute)
	c.Put("d", 200)
	if c.Len() != 1 || c.Bytes() != 200 {
		t.Fatalf("expired entries held: %d entries, %d bytes", c.Len(), c.Bytes())
	}
}

// Update sees the value kept and may decline to write.
func TestUpdate(t *testing.T) {
	c := New(Fixed(Bounds{Bytes: 1 << 20, TTL: time.Hour}), func(string, int) int64 { return 1 })
	c.Update("k", func(v int, ok bool) (int, bool) {
		if ok {
			t.Fatal("nothing kept yet")
		}
		return 1, true
	})
	c.Update("k", func(v int, ok bool) (int, bool) { return v + 1, ok })
	c.Update("k", func(v int, ok bool) (int, bool) { return 0, false })
	if v, _ := c.Get("k"); v != 2 {
		t.Fatalf("k = %d, want 2", v)
	}
	c.Remove("k")
	if _, ok := c.Get("k"); ok || c.Bytes() != 0 {
		t.Fatal("removed entry kept")
	}
	c.Put("x", 1)
	c.Clear()
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatal("cleared cache holds entries")
	}
}

// A named cache counts its reads and drops, and its totals leave once it is collected.
func TestStatsAndNamed(t *testing.T) {
	c := New(Fixed(Bounds{Bytes: 3*EntryBytes + 30, TTL: time.Hour}), func(string, int) int64 { return 10 }).Named("test-stats")
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Put("d", 4) // evicts a
	if _, ok := c.Get("a"); ok {
		t.Fatal("a kept")
	}
	if _, ok := c.Get("d"); !ok {
		t.Fatal("d not kept")
	}
	s := c.Stats()
	if s.Hits != 1 || s.Misses != 1 || s.Evicted != 1 || s.Entries != 3 || s.Bytes != 3*(EntryBytes+10) {
		t.Fatalf("stats = %+v", s)
	}
	if got := totals()["test-stats"]; got.Hits != 1 || got.Entries != 3 {
		t.Fatalf("totals = %+v", got)
	}
}
