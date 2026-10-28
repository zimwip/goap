package graph

import (
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// Caches are the bounds of what the graph keeps in memory between calls, one lib/cache each: the estimated bytes its
// entries hold, how long an entry no one reads is kept, the least recently read evicted first. A zero field takes the
// one of DefaultCaches.
type Caches struct {
	// Drafts: the views of the changes, folded from their logs (draftcache.go, ADR 0153).
	Drafts cache.Bounds
	// Versions: how a version holds its links and the links it holds, which never change once written (versioncache.go).
	Versions cache.Bounds
	// Headers: the headers of baselines (state.go).
	Headers cache.Bounds
	// Validations: the validations of the impacts of a change on a flow (impactvalidation.go).
	Validations cache.Bounds
	// Structures: the units and projects the guard found (guard.go): a node is never deleted, nor its type changed.
	Structures cache.Bounds
	// Heads: the heads of the lasting branches the views read (headHeader), each kept headAge at most.
	Heads cache.Bounds
}

// DefaultCacheTTL is how long the graph keeps an entry no one reads.
const DefaultCacheTTL = 5 * time.Minute

// DefaultCaches are the bounds of the caches of a graph that names none.
var DefaultCaches = Caches{
	Drafts:      cache.Bounds{Bytes: 64 << 20, TTL: DefaultCacheTTL},
	Versions:    cache.Bounds{Bytes: 32 << 20, TTL: DefaultCacheTTL},
	Headers:     cache.Bounds{Bytes: 4 << 20, TTL: DefaultCacheTTL},
	Validations: cache.Bounds{Bytes: 16 << 20, TTL: DefaultCacheTTL},
	Structures:  cache.Bounds{Bytes: 1 << 20, TTL: DefaultCacheTTL},
	Heads:       cache.Bounds{Bytes: 1 << 20, TTL: DefaultCacheTTL},
}

// boundsOf reads the bounds of one cache at each write: Graph.Caches may be set after New.
func (g *Graph) boundsOf(pick func(Caches) cache.Bounds) func() cache.Bounds {
	return func() cache.Bounds {
		b, d := pick(g.Caches), pick(DefaultCaches)
		if b.Bytes <= 0 {
			b.Bytes = d.Bytes
		}
		if b.TTL <= 0 {
			b.TTL = d.TTL
		}
		return b
	}
}

func (g *Graph) newCaches() {
	g.draftStates = newDraftCache(g.boundsOf(func(c Caches) cache.Bounds { return c.Drafts }))
	g.versions = newVersionCache(g.boundsOf(func(c Caches) cache.Bounds { return c.Versions }))
	g.headers = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Headers }), headerSize)
	g.heads = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Heads }), func(k string, h keptHead) int64 {
		return cache.StructBytes + int64(len(k)) + headerSize("", h.head)
	})
	g.validations = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Validations }), validationSize)
	g.foundRefs = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Structures }), func(k string, _ bool) int64 {
		return cache.SlotBytes + int64(len(k))
	})
}

func headerSize(_ domain.BaselineID, b domain.Baseline) int64 {
	n := cache.StructBytes + int64(len(b.ID)+len(b.Name)+len(b.Namespace)+len(b.Branch)+len(b.ParentID)+len(b.MergedFrom)+len(b.RebasedFrom)+len(b.ChangeID)+len(b.Kind))
	for id := range b.Nodes {
		n += cache.SlotBytes + cache.WordBytes + int64(len(id))
	}
	return n
}

func validationSize(_ validationKey, e validationEntry) int64 {
	n := int64(cache.SlotBytes)
	for id, v := range e.out {
		n += cache.SlotBytes + cache.StructBytes + int64(len(id))
		if v != nil {
			n += int64(len(v.Message))
		}
	}
	return n
}
