package mcpsvc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/algo"
)

// Library gives the adapter algorithms of the domain library (published domains of the registry).
type Library interface {
	// Algorithm returns the algorithm of a domain; an empty version is the latest published one.
	// It also returns the version it resolved.
	Algorithm(ctx context.Context, domain, version, name string) (algo.Algorithm, string, error)
}

// CachedLibrary remembers the algorithms it fetched. A pinned version is immutable once published,
// so it is kept as long as the process lives; an unpinned one (latest) only for TTL.
type CachedLibrary struct {
	Next Library
	TTL  time.Duration // for unpinned versions (default 30 s)
	Now  func() time.Time

	mu    sync.Mutex
	items map[string]cachedAlgo
}

type cachedAlgo struct {
	a       algo.Algorithm
	version string
	at      time.Time
	pinned  bool
}

var _ Library = (*CachedLibrary)(nil)

// Algorithm implements Library.
func (c *CachedLibrary) Algorithm(ctx context.Context, domain, version, name string) (algo.Algorithm, string, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	key := domain + "@" + version + "/" + name
	c.mu.Lock()
	if it, ok := c.items[key]; ok && (it.pinned || now.Sub(it.at) < ttl) {
		c.mu.Unlock()
		return it.a, it.version, nil
	}
	c.mu.Unlock()
	a, v, err := c.Next.Algorithm(ctx, domain, version, name)
	if err != nil {
		return a, v, fmt.Errorf("algorithm %s/%s: %w", domain, name, err)
	}
	c.mu.Lock()
	if c.items == nil {
		c.items = map[string]cachedAlgo{}
	}
	c.items[key] = cachedAlgo{a: a, version: v, at: now, pinned: version != ""}
	c.mu.Unlock()
	return a, v, nil
}
