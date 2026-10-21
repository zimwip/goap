package graph

import (
	"context"
	"slices"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// versionCache keeps what never changes once a version is written (ADR 0079, 0141): how it holds its links and the
// links it holds. A commit or a merge of N nodes reads them for the versions it starts from: read once, in one batch,
// they are not read again by the next operations on the same nodes. Only a version read by a transaction that did not
// write it is kept (what a transaction wrote may roll back, its number then taken again). A purge deletes versions
// (DeleteChange): it empties the cache, before and after its transaction, and a read that started before an emptying
// is not kept (gen). The entries are a lib/cache (Caches.Versions).
type versionCache struct {
	mu      sync.Mutex // orders a write with an emptying
	gen     uint64
	entries *cache.Cache[domain.NodeRef, versionEntry]
}

// versionEntry is what is known of a version: that it exists, how it holds its links, the links it holds.
type versionEntry struct {
	set      LinkSet
	hasSet   bool
	links    []domain.Link
	hasLinks bool
	exists   bool
}

func newVersionCache(bounds func() cache.Bounds) versionCache {
	return versionCache{entries: cache.New(bounds, func(_ domain.NodeRef, e versionEntry) int64 {
		n := int64(cache.StructBytes)
		for _, l := range e.links {
			n += cache.StructBytes + int64(len(l.ID)+len(l.Type)+len(l.From.ID)+len(l.To.ID)) + cache.Of(l.Properties)
		}
		return n
	})}
}

func (c *versionCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// reset empties the cache: the reads in flight are not kept.
func (c *versionCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.entries.Clear()
}

// update changes the entry of a version read at gen (nothing when the cache was emptied since).
func (c *versionCache) update(gen uint64, ref domain.NodeRef, fn func(e *versionEntry)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.entries.Update(ref, func(e versionEntry, _ bool) (versionEntry, bool) { fn(&e); return e, true })
}

// links are the links a version holds, when known (a copy).
func (c *versionCache) links(ref domain.NodeRef) ([]domain.Link, bool) {
	e, ok := c.entries.Get(ref)
	if !ok || !e.hasLinks || !e.exists {
		return nil, false
	}
	return cloneLinks(e.links), true
}

// set is how a version holds its links, when known.
func (c *versionCache) set(ref domain.NodeRef) (LinkSet, bool) {
	e, ok := c.entries.Get(ref)
	if !ok || !e.hasSet {
		return LinkSet{}, false
	}
	return e.set, true
}

func cloneLinks(ls []domain.Link) []domain.Link {
	if ls == nil {
		return nil
	}
	out := slices.Clone(ls)
	for i := range out {
		out[i].Properties = cloneMap(out[i].Properties)
	}
	return out
}

// fixed reports whether what the transaction reads of a version cannot change any more: an exact version it did not
// write.
func (t *guardTx) fixed(ref domain.NodeRef) bool { return ref.Version > 0 && !t.written[ref] }

// known records that a version exists, read at gen.
func (t *guardTx) known(gen uint64, ref domain.NodeRef) {
	t.g.versions.update(gen, ref, func(e *versionEntry) { e.exists = true })
}

// Node reads a version: an exact one the transaction did not write once per transaction (its origin may still be set,
// SetNodeOrigin, so it is not kept beyond).
func (t *guardTx) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	if !t.fixed(ref) {
		return t.Tx.Node(ctx, ref)
	}
	if n, ok := t.nodes[ref]; ok {
		return copyNode(n), nil
	}
	gen := t.g.versions.generation()
	n, err := t.Tx.Node(ctx, ref)
	if err != nil {
		return n, err
	}
	t.keepNode(gen, n)
	return copyNode(n), nil
}

func (t *guardTx) keepNode(gen uint64, n domain.Node) {
	if t.nodes == nil {
		t.nodes = map[domain.NodeRef]domain.Node{}
	}
	t.nodes[n.Ref()] = n
	t.known(gen, n.Ref())
}

// copyNode is a node its reader may change.
func copyNode(n domain.Node) domain.Node {
	n.Properties = cloneMap(n.Properties)
	n.Parents = slices.Clone(n.Parents)
	n.Origins = slices.Clone(n.Origins)
	return n
}

// OutLinks reads the links a version holds, kept for the versions that cannot change.
func (t *guardTx) OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	if !t.fixed(ref) {
		return t.Tx.OutLinks(ctx, ref)
	}
	if ls, ok := t.g.versions.links(ref); ok {
		return ls, nil
	}
	gen := t.g.versions.generation()
	ls, err := t.Tx.OutLinks(ctx, ref)
	if err != nil {
		return ls, err
	}
	// an empty set is kept only for a version known to exist: the links of a version not written yet are none until
	// it is
	kept := cloneLinks(ls)
	t.g.versions.update(gen, ref, func(e *versionEntry) {
		e.links, e.hasLinks = kept, true
		e.exists = e.exists || len(kept) > 0
	})
	return ls, nil
}

// LinkSetOf reads how a version holds its links, kept for the versions that cannot change.
func (t *guardTx) LinkSetOf(ctx context.Context, ref domain.NodeRef) (LinkSet, error) {
	if !t.fixed(ref) {
		return t.Tx.LinkSetOf(ctx, ref)
	}
	if s, ok := t.g.versions.set(ref); ok {
		return s, nil
	}
	gen := t.g.versions.generation()
	s, err := t.Tx.LinkSetOf(ctx, ref)
	if err != nil {
		return s, err
	}
	t.g.versions.update(gen, ref, func(e *versionEntry) { e.set, e.hasSet, e.exists = s, true, true })
	return s, nil
}

// prefetch reads in one batch the versions refs, their link sets and their links, for the operation that reads them
// one by one next (a commit, a merge, a review of N nodes reads N versions: three statements instead of 3N).
func (t *guardTx) prefetch(ctx context.Context, refs []domain.NodeRef) error {
	var nodes, links []domain.NodeRef
	seen := map[domain.NodeRef]bool{}
	for _, r := range refs {
		if !t.fixed(r) || seen[r] {
			continue
		}
		seen[r] = true
		if _, ok := t.nodes[r]; !ok {
			nodes = append(nodes, r)
		}
		_, hasLinks := t.g.versions.links(r)
		_, hasSet := t.g.versions.set(r)
		if !hasLinks || !hasSet {
			links = append(links, r)
		}
	}
	gen := t.g.versions.generation()
	if len(nodes) > 0 {
		ns, err := t.Tx.NodesAt(ctx, nodes, "")
		if err != nil {
			return err
		}
		for _, n := range ns {
			t.keepNode(gen, n)
		}
	}
	if len(links) == 0 {
		return nil
	}
	sets, err := t.Tx.LinkSetsOf(ctx, links)
	if err != nil {
		return err
	}
	ls, err := t.Tx.LinksFrom(ctx, links)
	if err != nil {
		return err
	}
	held := make(map[domain.NodeRef][]domain.Link, len(links))
	for _, l := range ls {
		held[l.From] = append(held[l.From], l) // in the order of their ids, as OutLinks reads them
	}
	for _, r := range links {
		s, ok := sets[r]
		if !ok {
			continue // no such version
		}
		kept := cloneLinks(held[r])
		t.g.versions.update(gen, r, func(e *versionEntry) {
			e.set, e.hasSet, e.links, e.hasLinks, e.exists = s, true, kept, true, true
		})
	}
	return nil
}

// prefetchVersions reads in one batch the versions refs and their links, when the transaction keeps them.
func prefetchVersions(ctx context.Context, tx Tx, refs []domain.NodeRef) error {
	if t, ok := tx.(*guardTx); ok && len(refs) > 0 {
		return t.prefetch(ctx, refs)
	}
	return nil
}
