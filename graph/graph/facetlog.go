package graph

import (
	"container/list"
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
)

// The entries the log facets fold (LogFacet) are kept per change like its drafts (draftcache.go): a read asks the log
// only for the entries after the last one kept, so that a blackboard of a change with a long history reads what happened
// since the previous one, not the whole log. What a transaction that wrote the log read is not kept (it may roll back).

// facetLog is the entries of each log facet of a change up to the position of each. Immutable once kept.
type facetLog struct {
	change  domain.ChangeID
	seq     map[string]int64
	entries map[string][]domain.LogEntry
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

// logEntries reads the entries of the log of a change each log facet folds: the ones kept, then the ones after them.
// The lists returned are shared: never modified.
func (g *Graph) logEntries(ctx context.Context, tx Tx, id domain.ChangeID) (map[string][]domain.LogEntry, error) {
	if len(g.LogFacets) == 0 {
		return nil, nil
	}
	kept := g.facetLogs.get(id)
	next := &facetLog{change: id, seq: map[string]int64{}, entries: make(map[string][]domain.LogEntry, len(g.LogFacets))}
	if kept != nil {
		maps.Copy(next.seq, kept.seq)
		maps.Copy(next.entries, kept.entries)
	}
	for name, f := range g.LogFacets {
		es, err := tx.Log(ctx, domain.LogFilter{Change: id, Types: f.Types, AfterSeq: next.seq[name]})
		if err != nil {
			return nil, err
		}
		if len(es) > 0 {
			next.entries[name] = append(slices.Clip(next.entries[name]), es...)
			next.seq[name] = es[len(es)-1].Seq
		}
	}
	if !wroteLog(tx, id) {
		g.facetLogs.put(next, g.draftCacheSize())
	}
	return next.entries, nil
}
