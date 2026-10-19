package graph

import (
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// TestVersionCache keeps what a read brought unless the cache was emptied while it ran, and keeps what is read again
// when the generations turn.
func TestVersionCache(t *testing.T) {
	var c versionCache
	a, b := domain.NodeRef{ID: "a", Version: 1}, domain.NodeRef{ID: "b", Version: 1}
	keep := func(gen uint64, ref domain.NodeRef, ls []domain.Link) {
		c.update(gen, ref, 4, func(e *versionEntry) { e.links, e.hasLinks, e.exists = ls, true, true })
	}
	gen := c.generation()
	keep(gen, a, []domain.Link{{ID: "l1", Properties: map[string]any{"x": 1}}})
	ls, ok := c.links(a)
	if !ok || len(ls) != 1 {
		t.Fatalf("links of a: %v %v", ls, ok)
	}
	ls[0].Properties["x"] = 2 // a copy
	if again, _ := c.links(a); again[0].Properties["x"] != 1 {
		t.Fatalf("the cache gave its own links: %v", again)
	}
	// a read that started before a purge is not kept
	gen = c.generation()
	c.reset()
	keep(gen, b, nil)
	if _, ok := c.links(b); ok {
		t.Fatal("a read from before the purge was kept")
	}
	if _, ok := c.links(a); ok {
		t.Fatal("the purge left an entry")
	}
	// an empty set is served only for a version known to exist
	gen = c.generation()
	c.update(gen, b, 4, func(e *versionEntry) { e.hasLinks = true })
	if _, ok := c.links(b); ok {
		t.Fatal("an empty set of a version not known to exist was served")
	}
	// a hot entry survives the turn of the generations
	keep(gen, a, nil)
	for i := range 6 {
		c.links(a)
		keep(gen, domain.NodeRef{ID: "n", Version: domain.Version(i + 1)}, nil)
	}
	if _, ok := c.links(a); !ok {
		t.Fatal("an entry read at each turn was dropped")
	}
}
