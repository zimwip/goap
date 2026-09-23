package engine

import (
	"context"
	"sync"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/metamodel"
	"github.com/zimwip/goap/pkg/methodology"
)

// SupertypesCache resolves NodeType ancestry from the metadata layer of the
// graph (ADR 0012), falling back to the methodology's declared schema
// (Methodology.Supertypes) when the graph has no NodeType nodes yet for that
// methodology — a permanent behavior, not a migration shim: it is what keeps
// unsynced graphs (unit tests, freshly seeded demos) working. Entries are
// cached per methodology and invalidated by a change of the main branch head.
type SupertypesCache struct {
	mu    sync.Mutex
	cache map[string]supertypesEntry
}

type supertypesEntry struct {
	head domain.BaselineID
	m    map[string][]string
}

// Get returns the ancestor map for m, from the graph when it has been
// synced, from the declared schema otherwise.
func (c *SupertypesCache) Get(ctx context.Context, g GraphPort, m *methodology.Compiled) map[string][]string {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		return m.Supertypes()
	}
	c.mu.Lock()
	if e, ok := c.cache[m.Name]; ok && e.head == head.ID {
		c.mu.Unlock()
		return e.m
	}
	c.mu.Unlock()

	out, err := metamodel.Supertypes(ctx, g, m.Name)
	if err != nil || out == nil {
		out = m.Supertypes()
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]supertypesEntry{}
	}
	c.cache[m.Name] = supertypesEntry{head: head.ID, m: out}
	c.mu.Unlock()
	return out
}

// Invalidate drops the cached entry of a methodology (called on a
// goap.graph.nodetype.changed event).
func (c *SupertypesCache) Invalidate(methodology string) {
	c.mu.Lock()
	delete(c.cache, methodology)
	c.mu.Unlock()
}
