package graph

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// versionCache keeps what never changes once a version is written (ADR 0079, 0141): the version, how it holds its links
// and the links it holds (a version is never rewritten, a merge included, ADR 0174 addendum). A commit or a merge of N nodes reads them for the versions it starts from: read once, in one batch,
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
	node     domain.Node
	hasNode  bool
	set      LinkSet
	hasSet   bool
	links    []domain.Link
	hasLinks bool
	exists   bool
}

func newVersionCache(bounds func() cache.Bounds) versionCache {
	return versionCache{entries: cache.New(bounds, func(_ domain.NodeRef, e versionEntry) int64 {
		n := int64(cache.StructBytes)
		if e.hasNode {
			n += cache.StructBytes + int64(len(e.node.ID)+len(e.node.Key)+len(e.node.Type)+len(e.node.Namespace)) + cache.Of(e.node.Properties)
		}
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

// node is the version, when kept (a copy).
func (c *versionCache) node(ref domain.NodeRef) (domain.Node, bool) {
	e, ok := c.entries.Get(ref)
	if !ok || !e.hasNode {
		return domain.Node{}, false
	}
	return copyNode(e.node), true
}

// forgetNode drops a version whose origin a transaction sets: read again, and the reads in flight are not kept.
func (c *versionCache) forgetNode(ref domain.NodeRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if e, ok := c.entries.Get(ref); ok && e.hasNode {
		e.node, e.hasNode = domain.Node{}, false
		c.entries.Put(ref, e)
	}
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

// Node reads a version: an exact one the transaction did not write once, kept between transactions.
func (t *guardTx) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	if ref.Version > 0 && t.written[ref] {
		// a version it wrote, read back in a batch (prefetch): kept for the transaction only
		if n, ok := t.nodes[ref]; ok {
			return copyNode(n), nil
		}
		return t.Tx.Node(ctx, ref)
	}
	if !t.fixed(ref) {
		return t.Tx.Node(ctx, ref)
	}
	if n, ok := t.nodes[ref]; ok {
		return copyNode(n), nil
	}
	if n, ok := t.g.versions.node(ref); ok {
		return n, nil
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
	kept := copyNode(n)
	t.g.versions.update(gen, n.Ref(), func(e *versionEntry) { e.node, e.hasNode, e.exists = kept, true, true })
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
	if ls, ok := t.wroteLinks[ref]; ok && ref.Version > 0 && t.written[ref] {
		return cloneLinks(ls), nil
	}
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
	var nodes, links, written []domain.NodeRef
	seen := map[domain.NodeRef]bool{}
	for _, r := range refs {
		if r.Version > 0 && t.written[r] && !seen[r] {
			// a version it wrote, read back once its links are written: kept for the transaction only, never shared (it may
			// roll back)
			seen[r] = true
			if _, ok := t.nodes[r]; !ok {
				nodes = append(nodes, r)
			}
			if _, ok := t.wroteLinks[r]; !ok {
				written = append(written, r)
			}
			continue
		}
		if !t.fixed(r) || seen[r] {
			continue
		}
		seen[r] = true
		if _, ok := t.nodes[r]; !ok {
			if n, ok := t.g.versions.node(r); ok {
				if t.nodes == nil {
					t.nodes = map[domain.NodeRef]domain.Node{}
				}
				t.nodes[r] = n
			} else {
				nodes = append(nodes, r)
			}
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
			if t.written[n.Ref()] {
				if t.nodes == nil {
					t.nodes = map[domain.NodeRef]domain.Node{}
				}
				t.nodes[n.Ref()] = n
				continue
			}
			t.keepNode(gen, n)
		}
	}
	if len(written) > 0 {
		ls, err := t.Tx.LinksFrom(ctx, written)
		if err != nil {
			return err
		}
		if t.wroteLinks == nil {
			t.wroteLinks = map[domain.NodeRef][]domain.Link{}
		}
		for _, r := range written {
			t.wroteLinks[r] = nil
		}
		for _, l := range ls {
			t.wroteLinks[l.From] = append(t.wroteLinks[l.From], l) // in the order of their ids, as OutLinks reads them
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
	if t, ok := unwrapGuard(tx); ok && len(refs) > 0 {
		return t.prefetch(ctx, refs)
	}
	return nil
}

// prefetchLatestOn reads at once the latest versions of some nodes on a branch, kept for LatestOn until the transaction
// writes or joins a version of the node: the versions it found.
func (t *guardTx) prefetchLatestOn(ctx context.Context, ids []domain.NodeID, branch string) ([]domain.NodeRef, error) {
	at, err := t.Tx.LatestVersionsOn(ctx, ids, branch)
	if err != nil {
		return nil, err
	}
	b := domain.BranchOf(branch)
	if t.latestOn == nil {
		t.latestOn = map[string]map[domain.NodeID]domain.Version{}
	}
	m := t.latestOn[b]
	if m == nil {
		m = map[domain.NodeID]domain.Version{}
		t.latestOn[b] = m
	}
	refs := make([]domain.NodeRef, 0, len(at))
	for _, id := range ids {
		v := at[id] // 0: none on the branch
		m[id] = v
		if v > 0 {
			refs = append(refs, domain.NodeRef{ID: id, Version: v})
		}
	}
	return refs, nil
}

// LatestOn reads the latest version of a node on a branch, from what prefetchLatestOn read when it holds the node.
func (t *guardTx) LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	b := domain.BranchOf(branch)
	if v, ok := t.latestOn[b][id]; ok {
		if v == 0 {
			return domain.Node{}, fmt.Errorf("node %s on %s: %w", id, b, ErrNotFound)
		}
		return t.Node(ctx, domain.NodeRef{ID: id, Version: v})
	}
	return t.Tx.LatestOn(ctx, id, branch)
}

// forgetLatest drops what prefetchLatestOn read of a node: the transaction wrote or joined a version of it.
func (t *guardTx) forgetLatest(id domain.NodeID) {
	for _, m := range t.latestOn {
		delete(m, id)
	}
}

// prefetchKeys resolves some keys of a namespace at once, kept for NodeIDByKey until the transaction writes a node of
// the key.
func (t *guardTx) prefetchKeys(ctx context.Context, namespace string, keys []string) error {
	ids, err := t.Tx.NodeIDsByKey(ctx, namespace, keys)
	if err != nil {
		return err
	}
	if t.keys == nil {
		t.keys = make(map[string]domain.NodeID, len(keys))
	}
	for _, k := range keys {
		t.keys[nsKey(namespace, k)] = ids[k] // "": no node holds it
	}
	return nil
}

// NodeIDByKey resolves a key, from what prefetchKeys read when it holds the key.
func (t *guardTx) NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error) {
	if id, ok := t.keys[nsKey(namespace, key)]; ok {
		if id == "" {
			return "", fmt.Errorf("node key %s: %w", key, ErrNotFound)
		}
		return id, nil
	}
	return t.Tx.NodeIDByKey(ctx, namespace, key)
}

// prefetchNodeKeys is prefetchKeys through the guard of the graph (nothing otherwise); forget drops what it read.
func prefetchNodeKeys(ctx context.Context, tx Tx, namespace string, keys []string) (forget func(), err error) {
	gt, ok := unwrapGuard(tx)
	if !ok || len(keys) < 2 {
		return func() {}, nil
	}
	return func() { gt.keys = nil }, gt.prefetchKeys(ctx, namespace, keys)
}
