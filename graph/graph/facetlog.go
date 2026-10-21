package graph

import (
	"container/list"
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
)

// What the log facets fold (LogFacet) is kept per change like its drafts (draftcache.go): a read asks the log only for
// the entries after the last one kept, so that a blackboard of a change with a long history reads what happened since
// the previous one, not the whole log. A facet with a Folder keeps its folder, each entry decoded once (ADR 0153);
// another keeps its entries. What a transaction that wrote the log read is not kept (it may roll back).

// facetLog is what each log facet of a change folds, up to the position of each. Immutable once kept.
type facetLog struct {
	change domain.ChangeID
	seq    map[string]int64
	reads  map[string]facetRead
}

// facetRead is what a log facet folds: its folder when it has one, else its entries (shared: never modified).
type facetRead struct {
	entries []domain.LogEntry
	folder  domain.LogFolder
}

type facetLogCache struct {
	mu sync.Mutex
	ll *list.List // of *facetLog, most recently used first
	m  map[domain.ChangeID]*list.Element
}

func (c *facetLogCache) get(id domain.ChangeID) *facetLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*facetLog)
	}
	return nil
}

func (c *facetLogCache) put(l *facetLog, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m, c.ll = map[domain.ChangeID]*list.Element{}, list.New()
	}
	if el, ok := c.m[l.change]; ok {
		el.Value = l
		c.ll.MoveToFront(el)
		return
	}
	c.m[l.change] = c.ll.PushFront(l)
	for c.ll.Len() > size {
		last := c.ll.Back()
		delete(c.m, last.Value.(*facetLog).change)
		c.ll.Remove(last)
	}
}

func (c *facetLogCache) drop(id domain.ChangeID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		c.ll.Remove(el)
		delete(c.m, id)
	}
}

// logEntries reads what each log facet of a change folds: what is kept, extended by the entries after it.
func (g *Graph) logEntries(ctx context.Context, tx Tx, id domain.ChangeID) (map[string]facetRead, error) {
	if len(g.LogFacets) == 0 {
		return nil, nil
	}
	kept := g.facetLogs.get(id)
	next := &facetLog{change: id, seq: map[string]int64{}, reads: make(map[string]facetRead, len(g.LogFacets))}
	if kept != nil {
		maps.Copy(next.seq, kept.seq)
		maps.Copy(next.reads, kept.reads)
	}
	for name, f := range g.LogFacets {
		es, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: f.Types, AfterSeq: next.seq[name]})
		if err != nil {
			return nil, err
		}
		r := next.reads[name]
		if f.Folder != nil && r.folder == nil {
			r.folder = f.Folder()
		}
		if len(es) > 0 {
			if r.folder != nil {
				// the kept folder stays as it is: the entries go to a copy
				r.folder = r.folder.Clone()
				if err := r.folder.Add(es); err != nil {
					return nil, err
				}
			} else {
				r.entries = append(slices.Clip(r.entries), es...)
			}
			next.seq[name] = es[len(es)-1].Seq
		}
		next.reads[name] = r
	}
	if !wroteLog(tx, id) {
		g.facetLogs.put(next, g.draftCacheSize())
	}
	return next.reads, nil
}
