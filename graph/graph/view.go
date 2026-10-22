package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// The reads of the current graph through a view (ADR 0157): a lasting branch at a baseline, or a change on a flow
// (its fork, its commits and its drafts). One world answers them all, read by node: what a read costs follows what it
// asks, never the size of the namespace.

// world is a view opened in a transaction.
type world struct {
	view View
	// baseline is the baseline the world starts from: the one of a branch view, the fork of a change.
	baseline domain.BaselineID
	// namespace is the namespace of the world.
	namespace string
	// change, flow and the rest are set for a change view only.
	change  *domain.Change
	flow    string
	branch  domain.Branch
	own     bool
	impacts []domain.ChangeImpact
	// over are the versions the change wrote over its fork (a draft is version 0), gone the nodes it deleted.
	over   map[domain.NodeID]domain.Version
	gone   map[domain.NodeID]bool
	reader *draftReader
}

// openWorld opens a view in a transaction.
func (g *Graph) openWorld(ctx context.Context, tx Tx, v View) (*world, error) {
	if err := v.Check(); err != nil {
		return nil, err
	}
	w := &world{view: v, reader: &draftReader{byNode: map[domain.NodeID]domain.Draft{}}}
	if !v.IsChange() {
		w.baseline, w.namespace = v.Baseline, domain.NamespaceOf(v.Namespace)
		if w.baseline == "" {
			head, err := branchHeadHeader(ctx, tx, w.namespace, v.Branch)
			if err != nil {
				return nil, err
			}
			w.baseline = head.ID
		} else {
			h, err := tx.BaselineHeader(ctx, w.baseline)
			if err != nil {
				return nil, err
			}
			if h.Namespace != w.namespace || h.Branch != v.Branch {
				return nil, fmt.Errorf("baseline %s is of %s/%s, not of %s: %w", w.baseline, h.Namespace, h.Branch, v, ErrInvalid)
			}
		}
		return w, nil
	}
	c, err := tx.Change(ctx, v.Change)
	if err != nil {
		return nil, err
	}
	w.change, w.flow, w.baseline, w.namespace = &c, c.ResolveFlow(v.Flow), c.BaselineID, domain.NamespaceOf(c.Namespace)
	if w.impacts, err = g.newFlowNodes(tx, c, w.flow).nodes(ctx); err != nil {
		return nil, err
	}
	if w.reader, err = g.newDraftReader(ctx, tx, c, w.flow, w.impacts); err != nil {
		return nil, err
	}
	// what the change wrote over its fork (changeViewTx at ViewWritten): a draft is version 0, a version deleted leaves
	// the world
	w.over, w.gone = map[domain.NodeID]domain.Version{}, map[domain.NodeID]bool{}
	for _, cn := range w.impacts {
		if cn.Post == nil || cn.Review == domain.ReviewRejected {
			continue
		}
		if cn.Post.IsDraft() {
			w.over[cn.Post.ID] = 0
			continue
		}
		n, err := tx.Node(ctx, *cn.Post)
		if err != nil {
			return nil, err
		}
		if n.Deleted {
			w.gone[n.ID] = true
		} else {
			w.over[n.ID] = n.Version
		}
	}
	if w.branch, w.own, err = ownBranch(ctx, tx, c); err != nil {
		return nil, err
	}
	return w, nil
}

// at reads the versions some nodes have in the world (absent: not in it).
func (w *world) at(ctx context.Context, tx Tx, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if w.change == nil {
		return versionsAt(ctx, tx, w.baseline, ids)
	}
	var at map[domain.NodeID]domain.Version
	var err error
	if w.own {
		at, err = branchVersions(ctx, tx, w.branch, ids)
	} else {
		at, err = versionsAt(ctx, tx, w.baseline, ids)
	}
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if v, ok := w.over[id]; ok {
			at[id] = v
		} else if w.gone[id] {
			delete(at, id)
		}
	}
	return at, nil
}

// node reads a node of the world at a version at returned (a draft for version 0).
func (w *world) node(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.Node, error) {
	return w.reader.node(ctx, tx, ref)
}

// drafts are the nodes the change holds a draft of, sorted.
func (w *world) drafts() []domain.NodeID {
	var out []domain.NodeID
	for id, v := range w.over {
		if v == 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// ref finds a node of the world by id or, when id is empty, by key: its version in the world.
func (w *world) ref(ctx context.Context, tx Tx, id domain.NodeID, key string) (domain.NodeRef, error) {
	if id == "" {
		if w.change != nil {
			for _, d := range w.reader.sortedDrafts() {
				if d.Key == key {
					id = d.Node
				}
			}
		}
		if id == "" {
			n, err := tx.NodeByKey(ctx, w.namespace, key)
			if err != nil {
				return domain.NodeRef{}, err
			}
			id = n.ID
		}
	}
	at, err := w.at(ctx, tx, []domain.NodeID{id})
	if err != nil {
		return domain.NodeRef{}, err
	}
	v, ok := at[id]
	if !ok {
		what := string(id)
		if key != "" {
			what = key
		}
		return domain.NodeRef{}, fmt.Errorf("node %s in %s: %w", what, w.view, ErrNotFound)
	}
	return domain.NodeRef{ID: id, Version: v}, nil
}

// ViewNode reads a node as a view holds it, by id or, when id is empty, by key: with its links, the latest version of
// the node anywhere for reference (ADR 0157).
func (g *Graph) ViewNode(ctx context.Context, v View, id domain.NodeID, key string) (nv domain.NodeView, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v)
		if err != nil {
			return err
		}
		ref, err := w.ref(ctx, tx, id, key)
		if err != nil {
			return err
		}
		nv, err = viewIn(ctx, tx, w.reader, ref)
		return err
	})
	return
}

// ViewNeighbourhood returns a node as a view holds it, with its direct neighbours: the nodes and the links between
// them, read node by node (ADR 0137, 0157). A link is kept when its other end is in the world, at whatever version it
// names; it is suspect when it names a version of its target the world does not hold.
func (g *Graph) ViewNeighbourhood(ctx context.Context, v View, nodeID domain.NodeID) (nb Neighbourhood, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v)
		if err != nil {
			return err
		}
		nb, err = w.neighbourhood(ctx, tx, nodeID)
		return err
	})
	return
}

func (w *world) neighbourhood(ctx context.Context, tx Tx, nodeID domain.NodeID) (nb Neighbourhood, err error) {
	at, err := w.at(ctx, tx, []domain.NodeID{nodeID})
	if err != nil {
		return nb, err
	}
	v, ok := at[nodeID]
	if !ok {
		return nb, fmt.Errorf("node %s in %s: %w", nodeID, w.view, ErrNotFound)
	}
	ref := domain.NodeRef{ID: nodeID, Version: v}
	if nb.Node, err = w.node(ctx, tx, ref); err != nil {
		return nb, err
	}
	// Outgoing links belong to the source version: all of them are the node's. Incoming links may target any version
	// of the node (suspect links): keep those whose source is at the version the world holds. The drafts of a change
	// hold links into the node too.
	out, err := w.reader.outLinks(ctx, tx, ref)
	if err != nil {
		return nb, err
	}
	in, err := tx.LinksTo(ctx, nodeID)
	if err != nil {
		return nb, err
	}
	for _, d := range w.drafts() {
		if d == nodeID {
			continue
		}
		dl, err := w.reader.outLinks(ctx, tx, domain.NodeRef{ID: d})
		if err != nil {
			return nb, err
		}
		for _, l := range dl {
			if !l.External() && l.To.ID == nodeID {
				in = append(in, l)
			}
		}
	}
	var near []domain.NodeID
	for _, l := range out {
		if !l.External() {
			near = append(near, l.To.ID)
		}
	}
	for _, l := range in {
		near = append(near, l.From.ID)
	}
	slices.Sort(near)
	if at, err = w.at(ctx, tx, slices.Compact(near)); err != nil {
		return nb, err
	}
	seen := map[domain.NodeID]bool{nodeID: true}
	add := func(r domain.NodeRef) error {
		if seen[r.ID] {
			return nil
		}
		seen[r.ID] = true
		n, err := w.node(ctx, tx, r)
		if err != nil {
			return err
		}
		nb.Nodes = append(nb.Nodes, n)
		return nil
	}
	for _, l := range out {
		if l.External() {
			nb.Links = append(nb.Links, l)
			continue
		}
		to := l.To
		if tv, ok := at[to.ID]; ok {
			if tv != to.Version && tv != 0 && to.Version != 0 {
				nb.Suspect = append(nb.Suspect, l.ID)
			}
			to.Version = tv
		} else if to.IsDraft() {
			continue // a draft the change no longer holds
		}
		if err := add(to); err != nil {
			return nb, err
		}
		nb.Links = append(nb.Links, l)
	}
	slices.SortStableFunc(in, func(a, b domain.Link) int { return int(a.To.Version) - int(b.To.Version) })
	taken := map[domain.LinkID]bool{}
	for _, l := range in {
		if l.From.ID == nodeID || taken[l.ID] {
			continue // a self link is already among the outgoing ones
		}
		if v != 0 && l.To.Version > v {
			continue // a link into a later version of the node than the world holds
		}
		if fv, ok := at[l.From.ID]; !ok || fv != l.From.Version {
			continue // not the version of its source the world holds
		}
		if l.To.Version != 0 && v != 0 && l.To.Version != v {
			nb.Suspect = append(nb.Suspect, l.ID)
		}
		taken[l.ID] = true
		if err := add(l.From); err != nil {
			return nb, err
		}
		nb.Links = append(nb.Links, l)
	}
	return nb, nil
}

// ViewNodes returns a page of the nodes of a view ordered by key, matching q (ADR 0157): for a branch view the page of
// its baseline (BaselineNodes); for a change view the page of its fork with what the change wrote laid over it (its
// commits, its drafts, the nodes it created), read for those nodes only.
func (g *Graph) ViewNodes(ctx context.Context, v View, q NodeQuery) (page NodePage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v)
		if err != nil {
			return err
		}
		page, err = w.page(ctx, tx, q)
		page.Baseline = w.baseline
		return err
	})
	return page, err
}

// page is a page of the nodes of the world.
func (w *world) page(ctx context.Context, tx Tx, q NodeQuery) (NodePage, error) {
	if w.change != nil {
		return w.nodes(ctx, tx, q)
	}
	if q.Change != "" {
		nodes, err := changeNodesIn(ctx, tx, w.baseline, q.Change)
		return pageNodes(nodes, q), err
	}
	if w.baseline == "" {
		return NodePage{}, nil
	}
	b, err := tx.Baseline(ctx, w.baseline)
	if err != nil || len(b.Nodes) == 0 {
		return NodePage{}, err
	}
	return tx.NodePage(ctx, refsOf(b.Nodes), q)
}

// nodes is the page of a change view: the fork's nodes the change left alone, paged by the store, merged with the
// nodes the change holds another version of (read node by node).
func (w *world) nodes(ctx context.Context, tx Tx, q NodeQuery) (NodePage, error) {
	if q.Change != "" { // the nodes the impacts of a change act on, as the fork has them
		nodes, err := changeNodesIn(ctx, tx, w.baseline, q.Change)
		return pageNodes(nodes, q), err
	}
	// the nodes of the world the change wrote: their versions in it
	var mine []domain.NodeID
	if w.own {
		ids, err := tx.BranchNodeIDs(ctx, w.branch.Name)
		if err != nil {
			return NodePage{}, err
		}
		mine = append(mine, ids...)
	}
	for id := range w.over {
		mine = append(mine, id)
	}
	for id := range w.gone {
		mine = append(mine, id)
	}
	slices.Sort(mine)
	mine = slices.Compact(mine)
	at, err := w.at(ctx, tx, mine)
	if err != nil {
		return NodePage{}, err
	}
	var written []domain.Node
	for _, id := range mine {
		ver, ok := at[id]
		if !ok {
			continue
		}
		n, err := w.node(ctx, tx, domain.NodeRef{ID: id, Version: ver})
		if err != nil {
			return NodePage{}, err
		}
		written = append(written, n)
	}

	// the fork, the nodes the change wrote left out, paged with room for them
	var page NodePage
	if w.baseline != "" {
		b, err := tx.Baseline(ctx, w.baseline)
		if err != nil {
			return NodePage{}, err
		}
		refs := map[domain.NodeID]domain.Version{}
		for id, v := range b.Nodes {
			if _, ok := slices.BinarySearch(mine, id); !ok {
				refs[id] = v
			}
		}
		if len(refs) > 0 {
			wide := q
			wide.Offset, wide.Limit = 0, q.Offset+q.Limit
			if page, err = tx.NodePage(ctx, refsOf(refs), wide); err != nil {
				return NodePage{}, err
			}
		}
	}
	extra := pageNodes(written, NodeQuery{Type: q.Type, Text: q.Text, IncludeDeleted: q.IncludeDeleted, Limit: MaxBrowseLimit})
	all := append(page.Nodes, extra.Nodes...)
	slices.SortFunc(all, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	counts := map[string]int{}
	for _, t := range append(page.Types, extra.Types...) {
		counts[t.Type] += t.Count
	}
	out := NodePage{Total: page.Total + extra.Total}
	out.Types, _ = typeCounts(counts, "")
	if q.Offset < len(all) {
		out.Nodes = all[q.Offset:min(q.Offset+q.Limit, len(all))]
	}
	return out, nil
}
