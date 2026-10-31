package graph

import (
	"fmt"
	"strconv"
	"strings"
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
	// Headers: the headers of commits (state.go).
	Headers cache.Bounds
	// Validations: the validations of the impacts of a change on a flow (impactvalidation.go).
	Validations cache.Bounds
	// Structures: the units and projects the guard found (guard.go): a node is never deleted, nor its type changed.
	Structures cache.Bounds
	// Selections: the versions of the nodes at the heads of the lasting branches the views read (heads.go), one entry
	// per node and branch: a node missing costs a statement each time a view reads it by key.
	Selections cache.Bounds
	// KeyIDs: the nodes of the keys (heads.go, guard.go), one entry per node.
	KeyIDs cache.Bounds
}

// DefaultCacheTTL is how long the graph keeps an entry no one reads.
const DefaultCacheTTL = 5 * time.Minute

// DefaultCaches are the bounds of the caches of a graph that names none.
var DefaultCaches = Caches{
	Drafts:      cache.Bounds{Bytes: 64 << 20, TTL: DefaultCacheTTL},
	Versions:    cache.Bounds{Bytes: 128 << 20, TTL: DefaultCacheTTL},
	Headers:     cache.Bounds{Bytes: 16 << 20, TTL: DefaultCacheTTL},
	Validations: cache.Bounds{Bytes: 16 << 20, TTL: DefaultCacheTTL},
	Structures:  cache.Bounds{Bytes: 1 << 20, TTL: DefaultCacheTTL},
	Selections:  cache.Bounds{Bytes: 128 << 20, TTL: DefaultCacheTTL},
	KeyIDs:      cache.Bounds{Bytes: 64 << 20, TTL: DefaultCacheTTL},
}

// cacheFields are the caches of Caches by the name ParseCaches reads.
var cacheFields = map[string]func(*Caches) *cache.Bounds{
	"drafts":      func(c *Caches) *cache.Bounds { return &c.Drafts },
	"versions":    func(c *Caches) *cache.Bounds { return &c.Versions },
	"headers":     func(c *Caches) *cache.Bounds { return &c.Headers },
	"validations": func(c *Caches) *cache.Bounds { return &c.Validations },
	"structures":  func(c *Caches) *cache.Bounds { return &c.Structures },
	"selections":  func(c *Caches) *cache.Bounds { return &c.Selections },
	"key_ids":     func(c *Caches) *cache.Bounds { return &c.KeyIDs },
}

// ParseCaches reads bounds written "<cache>=<MiB>[,...]" (GOAP_GRAPH_CACHES), the caches named as their metrics
// (graph.<cache>) and "ttl=<duration>" for all of them; what it does not name keeps DefaultCaches'.
func ParseCaches(spec string) (Caches, error) {
	var c Caches
	var ttl time.Duration
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok {
			return c, fmt.Errorf("graph caches: %q is not <cache>=<MiB>", part)
		}
		if name == "ttl" {
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return c, fmt.Errorf("graph caches: ttl %q is not a positive duration", value)
			}
			ttl = d
			continue
		}
		field, known := cacheFields[strings.TrimPrefix(name, "graph.")]
		if !known {
			return c, fmt.Errorf("graph caches: unknown cache %q", name)
		}
		mib, err := strconv.ParseInt(value, 10, 64)
		if err != nil || mib <= 0 {
			return c, fmt.Errorf("graph caches: %s=%q is not a positive number of MiB", name, value)
		}
		field(&c).Bytes = mib << 20
	}
	if ttl > 0 {
		for _, field := range cacheFields {
			field(&c).TTL = ttl
		}
	}
	return c, nil
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
	g.headers = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Headers }), headerSize).Named("graph.headers")
	g.selections = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Selections }), selSize).Named("graph.selections")
	g.keyIDs = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.KeyIDs }), keyIDSize).Named("graph.key_ids")
	g.validations = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Validations }), validationSize).Named("graph.validations")
	g.foundRefs = cache.New(g.boundsOf(func(c Caches) cache.Bounds { return c.Structures }), func(k string, _ bool) int64 {
		return cache.SlotBytes + int64(len(k))
	}).Named("graph.structures")
}

func headerSize(_ domain.CommitID, b domain.Commit) int64 {
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
