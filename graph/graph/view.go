package graph

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// The reads of the current graph through a view (ADR 0157): a lasting branch at a commit, or a change on a flow
// (its fork, its commits and its drafts). One world answers them all, read by node: what a read costs follows what it
// asks, never the size of the namespace.

// world is a view opened in a transaction.
type world struct {
	view View
	// commit is the commit the world starts from: the one of a branch view, the fork of a change.
	commit domain.CommitID
	// namespace is the namespace of the world.
	namespace string
	// change, flow and the rest are set for a change view only.
	change  *domain.Change
	flow    string
	branch  domain.Branch
	own     bool
	impacts domain.Impacts
	// over are the versions the change wrote over its fork (a draft is version 0), gone the nodes it deleted.
	over   map[domain.NodeID]domain.Version
	gone   map[domain.NodeID]bool
	reader *draftReader
	// reach is what the caller reads (nil: everything), vis what it was found to read of the nodes met
	reach *Reach
	vis   map[domain.NodeRef]bool
	// filter is the reach as the stores filter a page (nil: every node)
	filter *graphapi.ReachFilter
	// entering is how the world reads the links entering its nodes, decided at the first read (enteringPlan)
	entering *enteringPlan
}

// openWorld opens a view in a transaction, read by a caller of some reach (nil: everything; asked of g.reachOf before
// the transaction).
func (g *Graph) openWorld(ctx context.Context, tx Tx, v View, reach *Reach) (*world, error) {
	if err := v.Check(); err != nil {
		return nil, err
	}
	w := &world{view: v, reader: &draftReader{byNode: map[domain.NodeID]domain.Draft{}}, vis: map[domain.NodeRef]bool{}}
	if !v.IsChange() {
		w.commit, w.namespace = v.Commit, domain.NamespaceOf(v.Namespace)
		w.reach = reach.in(w.namespace)
		w.filter = g.filterOf(w.reach)
		if w.commit == "" {
			head, err := g.headHeader(ctx, tx, w.namespace, v.Branch)
			if err != nil {
				return nil, err
			}
			w.commit = head.ID
		} else {
			h, err := tx.CommitHeader(ctx, w.commit)
			if err != nil {
				return nil, err
			}
			if h.Namespace != w.namespace || h.Branch != v.Branch {
				return nil, fmt.Errorf("commit %s is of %s/%s, not of %s: %w", w.commit, h.Namespace, h.Branch, v, ErrInvalid)
			}
		}
		return w, nil
	}
	c, err := tx.Change(ctx, v.Change)
	if err != nil {
		return nil, err
	}
	w.change, w.flow, w.commit, w.namespace = &c, c.ResolveFlow(v.Flow), c.ForkCommit, domain.NamespaceOf(c.Namespace)
	w.reach = reach.in(w.namespace)
	w.filter = g.filterOf(w.reach)
	if w.impacts, err = g.newFlowNodes(tx, c, w.flow).nodes(ctx); err != nil {
		return nil, err
	}
	if w.reader, err = g.newDraftReader(ctx, tx, c, w.flow, w.impacts); err != nil {
		return nil, err
	}
	// what the change wrote over its fork (changeViewTx at ViewWritten): a draft is version 0, a version deleted leaves
	// the world
	w.over, w.gone = map[domain.NodeID]domain.Version{}, map[domain.NodeID]bool{}
	posts, err := storedPosts(ctx, tx, w.impacts, false)
	if err != nil {
		return nil, err
	}
	for _, cn := range w.impacts.All() {
		if cn.Post == nil || cn.Review == domain.ReviewRejected {
			continue
		}
		if cn.Post.IsDraft() {
			w.over[cn.Post.ID] = 0
			continue
		}
		n := posts[*cn.Post]
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
		return versionsAt(ctx, tx, w.commit, ids)
	}
	var at map[domain.NodeID]domain.Version
	var err error
	if w.own {
		at, err = branchVersions(ctx, tx, w.branch, ids)
	} else {
		at, err = versionsAt(ctx, tx, w.commit, ids)
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
		if id == "" && w.change == nil {
			// the node of the key and its version in the state, in one statement when the store reads them so
			found, v, decided, err := selectKey(ctx, tx, w.commit, key)
			if err != nil {
				return domain.NodeRef{}, err
			}
			if decided && found == "" {
				return domain.NodeRef{}, fmt.Errorf("node key %s: %w", key, ErrNotFound)
			}
			if decided {
				return w.found(ctx, tx, found, key, v, v > 0)
			}
		}
		if id == "" {
			var err error
			if id, err = tx.NodeIDByKey(ctx, w.namespace, key); err != nil {
				return domain.NodeRef{}, err
			}
		}
	}
	at, err := w.at(ctx, tx, []domain.NodeID{id})
	if err != nil {
		return domain.NodeRef{}, err
	}
	v, ok := at[id]
	return w.found(ctx, tx, id, key, v, ok)
}

// found is the reference of a node of the world at version v when ok, the caller's reach seeing it; not found else.
func (w *world) found(ctx context.Context, tx Tx, id domain.NodeID, key string, v domain.Version, ok bool) (domain.NodeRef, error) {
	if ok && w.reach != nil {
		n, err := w.stored(ctx, tx, domain.NodeRef{ID: id, Version: v})
		if err != nil {
			return domain.NodeRef{}, err
		}
		ok = w.sees(n)
	}
	if !ok {
		what := string(id)
		if key != "" {
			what = key
		}
		return domain.NodeRef{}, fmt.Errorf("node %s in %s: %w", what, w.view, ErrNotFound)
	}
	return domain.NodeRef{ID: id, Version: v}, nil
}

// stored reads a version a view of a branch holds through the latest version of its node on main, which a view
// shows with it (viewIn) and which is that version when the view is main at its head: one statement for both. A
// change view reads it as w.node does.
func (w *world) stored(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.Node, error) {
	if w.change != nil || ref.IsDraft() {
		return w.node(ctx, tx, ref)
	}
	if _, ok := w.reader.latest[ref.ID]; !ok {
		l, err := tx.Node(ctx, domain.NodeRef{ID: ref.ID})
		if err != nil && !isNotFound(err) {
			return domain.Node{}, err
		}
		if w.reader.latest == nil {
			w.reader.latest = map[domain.NodeID]domain.Version{}
		}
		w.reader.latest[ref.ID] = l.Version
		if err == nil && l.Version == ref.Version {
			if gt, ok := unwrapGuard(tx); ok && gt.fixed(ref) {
				gt.keepNode(gt.g.versions.generation(), l)
			}
			return l, nil
		}
	}
	return w.node(ctx, tx, ref)
}

// ViewNode reads a node as a view holds it, by id or, when id is empty, by key: with its links, the latest version of
// the node anywhere for reference (ADR 0157).
func (g *Graph) ViewNode(ctx context.Context, v View, id domain.NodeID, key string) (nv domain.NodeView, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return nv, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil {
			return err
		}
		ref, err := w.ref(ctx, tx, id, key)
		if err != nil {
			return err
		}
		if err := w.preloadIn(ctx, tx, ref); err != nil {
			return err
		}
		// the version and the latest of its node read together
		if _, err := w.stored(ctx, tx, ref); err != nil {
			return err
		}
		if nv, err = viewIn(ctx, tx, w.reader, ref); err != nil {
			return err
		}
		nv.Out, nv.In, err = w.keepLinks(ctx, tx, nv.Out, nv.In)
		return err
	})
	return
}

// ViewNeighbourhood returns a node as a view holds it, with its direct neighbours: the nodes and the links between
// them, read node by node (ADR 0137, 0157). A link is kept when its other end is in the world, at whatever version it
// names; it is suspect when it names a version of its target the world does not hold.
func (g *Graph) ViewNeighbourhood(ctx context.Context, v View, nodeID domain.NodeID) (Neighbourhood, error) {
	return g.ViewNeighbourhoodOf(ctx, v, nodeID, nil)
}

// ViewNeighbourhoodOf is ViewNeighbourhood keeping the links of some types (nil: every type): a hop of a navigation,
// which reads those link types only, by the indexes of the links by source or target and type (a query's hop, ADR
// 0160). A type is matched as written or, unqualified, qualified by the namespace of the view.
func (g *Graph) ViewNeighbourhoodOf(ctx context.Context, v View, nodeID domain.NodeID, types []string) (nb Neighbourhood, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return nb, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil {
			return err
		}
		nb, err = w.neighbourhood(ctx, tx, nodeID, w.linkTypes(types))
		return err
	})
	return
}

// linkTypes are the link types asked as the store holds them: as written, and an unqualified one also qualified by
// the namespace of the world (nil stays nil: every type).
func (w *world) linkTypes(types []string) []string {
	if types == nil {
		return nil
	}
	out := []string{}
	for _, t := range types {
		out = append(out, t)
		if !strings.Contains(t, "@") {
			out = append(out, w.namespace+"@"+t)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (w *world) neighbourhood(ctx context.Context, tx Tx, nodeID domain.NodeID, types []string) (Neighbourhood, error) {
	at, err := w.at(ctx, tx, []domain.NodeID{nodeID})
	if err != nil {
		return Neighbourhood{}, err
	}
	v, ok := at[nodeID]
	if !ok {
		return Neighbourhood{}, fmt.Errorf("node %s in %s: %w", nodeID, w.view, ErrNotFound)
	}
	n, err := w.node(ctx, tx, domain.NodeRef{ID: nodeID, Version: v})
	if err != nil {
		return Neighbourhood{}, err
	}
	if !w.sees(n) {
		return Neighbourhood{}, fmt.Errorf("node %s in %s: %w", nodeID, w.view, ErrNotFound)
	}
	nbs, err := w.neighbourhoods(ctx, tx, []domain.Node{n}, types, bothWays)
	return nbs[nodeID], err
}

// linkWays says which links of a node a neighbourhood reads: those leaving it, those entering it, or both.
type linkWays int

const (
	bothWays linkWays = iota
	leaving
	entering
)

// neighbourhoods reads the neighbourhoods of several nodes of the world, as it holds them and the caller reads them, in
// one batch (each read of the store once for them all, never once per node: a hop of a query reads its frontier so),
// keeping the links of some types (nil: every type) in some ways.
func (w *world) neighbourhoods(ctx context.Context, tx Tx, centres []domain.Node, types []string, ways linkWays) (map[domain.NodeID]Neighbourhood, error) {
	out := make(map[domain.NodeID]Neighbourhood, len(centres))
	held := map[domain.NodeID]domain.NodeRef{} // the version of each node read, by node
	ids := make([]domain.NodeID, 0, len(centres))
	var stored []domain.NodeRef
	for _, n := range centres {
		ref := n.Ref()
		out[ref.ID], held[ref.ID] = Neighbourhood{Node: n}, ref
		ids = append(ids, ref.ID)
		if !ref.IsDraft() {
			stored = append(stored, ref)
		}
	}
	if len(held) == 0 {
		return out, nil
	}
	var err error
	keep := func(l domain.Link) bool { return types == nil || slices.Contains(types, l.Type) }
	// Outgoing links belong to the source version: all of them are the node's. Incoming links may target any version
	// of the node (suspect links): keep those whose source is at the version the world holds. The drafts of a change
	// hold links into the node too. A hop of some types reads those types only (LinksFromOfType, LinkSources).
	outOf, inOf := map[domain.NodeID][]domain.Link{}, map[domain.NodeID][]domain.Link{}
	if ways != entering {
		switch {
		case types == nil:
			if err := prefetchVersions(ctx, tx, stored); err != nil {
				return nil, err
			}
			for id, ref := range held {
				if outOf[id], err = w.reader.outLinks(ctx, tx, ref); err != nil {
					return nil, err
				}
			}
		case len(types) > 0:
			for id, ref := range held {
				if ref.IsDraft() {
					if outOf[id], err = w.reader.outLinks(ctx, tx, ref); err != nil {
						return nil, err
					}
				}
			}
			if len(stored) > 0 {
				ls, err := tx.LinksFromOfType(ctx, stored, types)
				if err != nil {
					return nil, err
				}
				for _, l := range ls {
					outOf[l.From.ID] = append(outOf[l.From.ID], l)
				}
			}
		}
	}
	if ways != leaving && (types == nil || len(types) > 0) {
		targets := make([]domain.NodeID, 0, len(held))
		for id := range held {
			targets = append(targets, id)
		}
		slices.Sort(targets)
		if inOf, err = w.enteringOf(ctx, tx, targets, types); err != nil {
			return nil, err
		}
		for _, d := range w.drafts() {
			dl, err := w.reader.outLinks(ctx, tx, domain.NodeRef{ID: d})
			if err != nil {
				return nil, err
			}
			for _, l := range dl {
				// a link of a draft to itself is among its outgoing links, unless they are not read
				if _, ok := held[l.To.ID]; ok && !l.External() && (l.To.ID != d || ways == entering) && keep(l) {
					inOf[l.To.ID] = append(inOf[l.To.ID], l)
				}
			}
		}
	}
	for id := range held {
		outOf[id] = slices.DeleteFunc(outOf[id], func(l domain.Link) bool { return !keep(l) })
		inOf[id] = slices.DeleteFunc(inOf[id], func(l domain.Link) bool { return !keep(l) })
	}
	return w.complete(ctx, tx, ids, held, out, outOf, inOf, ways)
}

// complete reads the nodes at the other ends of the links of some nodes read at held, and lays them into their
// neighbourhoods out.
func (w *world) complete(ctx context.Context, tx Tx, ids []domain.NodeID, held map[domain.NodeID]domain.NodeRef, out map[domain.NodeID]Neighbourhood,
	outOf, inOf map[domain.NodeID][]domain.Link, ways linkWays) (map[domain.NodeID]Neighbourhood, error) {
	// the version the world holds of each source of an entering link is the one the link was read from (a draft: 0);
	// only the targets of the outgoing links are asked of the world
	known := map[domain.NodeID]domain.Version{}
	for id := range held {
		for _, l := range inOf[id] {
			if v, ok := w.over[l.From.ID]; ok && v == 0 {
				known[l.From.ID] = 0
			} else {
				known[l.From.ID] = l.From.Version
			}
		}
	}
	var ends []domain.NodeID
	for id := range held {
		for _, l := range outOf[id] {
			if _, ok := known[l.To.ID]; !ok && !l.External() {
				ends = append(ends, l.To.ID)
			}
		}
	}
	at := known
	if len(ends) > 0 {
		slices.Sort(ends)
		got, err := w.at(ctx, tx, slices.Compact(ends))
		if err != nil {
			return nil, err
		}
		maps.Copy(at, got)
	}
	// the neighbours are read in one batch (one statement for the stored ones), never one by one: a hub has thousands
	var nrefs []domain.NodeRef
	for id := range held {
		for _, l := range outOf[id] {
			if l.External() {
				continue
			}
			if tv, ok := at[l.To.ID]; ok {
				nrefs = append(nrefs, domain.NodeRef{ID: l.To.ID, Version: tv})
			} else if !l.To.IsDraft() {
				nrefs = append(nrefs, l.To)
			}
		}
		for _, l := range inOf[id] {
			if fv, ok := at[l.From.ID]; ok && fv == l.From.Version && l.From.ID != id {
				nrefs = append(nrefs, l.From)
			}
		}
	}
	near, err := w.reader.nodes(ctx, tx, nrefs)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		ref, ok := held[id]
		if !ok {
			continue
		}
		nb := out[id]
		if err := w.assemble(ctx, tx, &nb, ref, outOf[id], inOf[id], ways == entering, at, near); err != nil {
			return nil, err
		}
		out[id] = nb
	}
	return out, nil
}

// assemble lays the links of a node read at ref into its neighbourhood with the nodes at their other ends, at the
// versions at the world holds them (read already in near). A link to itself is among the outgoing links, or among the
// entering ones when selfIn (the outgoing ones not read).
func (w *world) assemble(ctx context.Context, tx Tx, nb *Neighbourhood, ref domain.NodeRef, out, in []domain.Link, selfIn bool,
	at map[domain.NodeID]domain.Version, near map[domain.NodeRef]domain.Node) error {
	nodeID, v := ref.ID, ref.Version
	// a neighbour the caller does not read is left out with the links to it (ADR 0161 §5: no stub)
	seen := map[domain.NodeID]bool{nodeID: true}
	add := func(r domain.NodeRef) (bool, error) {
		if v, ok := seen[r.ID]; ok {
			return v, nil
		}
		n, ok := near[r]
		if !ok {
			var err error
			if n, err = w.node(ctx, tx, r); err != nil {
				return false, err
			}
		}
		seen[r.ID] = w.sees(n)
		if seen[r.ID] {
			nb.Nodes = append(nb.Nodes, n)
		}
		return seen[r.ID], nil
	}
	for _, l := range out {
		if l.External() {
			nb.Links = append(nb.Links, l)
			continue
		}
		to := l.To
		suspect := false
		if tv, ok := at[to.ID]; ok {
			suspect = tv != to.Version && tv != 0 && to.Version != 0
			to.Version = tv
		} else if to.IsDraft() {
			continue // a draft the change no longer holds
		}
		if ok, err := add(to); err != nil {
			return err
		} else if !ok {
			continue
		}
		if suspect {
			nb.Suspect = append(nb.Suspect, l.ID)
		}
		nb.Links = append(nb.Links, l)
	}
	slices.SortStableFunc(in, func(a, b domain.Link) int { return int(a.To.Version) - int(b.To.Version) })
	taken := map[domain.LinkID]bool{}
	for _, l := range in {
		if (l.From.ID == nodeID && !selfIn) || taken[l.ID] {
			continue // a self link is already among the outgoing ones
		}
		if v != 0 && l.To.Version > v {
			continue // a link into a later version of the node than the world holds
		}
		if fv, ok := at[l.From.ID]; !ok || fv != l.From.Version {
			continue // not the version of its source the world holds
		}
		if ok, err := add(l.From); err != nil {
			return err
		} else if !ok {
			continue
		}
		if l.To.Version != 0 && v != 0 && l.To.Version != v {
			nb.Suspect = append(nb.Suspect, l.ID)
		}
		taken[l.ID] = true
		nb.Links = append(nb.Links, l)
	}
	return nil
}

// preloadIn gives the reader the stored links entering the version of a node it will read (the one a draft was
// checked out from for a draft) from the versions of their sources the world holds (entering), rather than from every
// version of every source that ever held one.
func (w *world) preloadIn(ctx context.Context, tx Tx, ref domain.NodeRef) error {
	if d, ok := w.reader.get(ref.ID); ok && ref.IsDraft() && d.Base != nil {
		ref = *d.Base
	}
	if ref.IsDraft() {
		return nil
	}
	entering, err := w.enteringOf(ctx, tx, []domain.NodeID{ref.ID}, nil)
	if err != nil {
		return err
	}
	links := entering[ref.ID]
	in := []domain.Link{}
	for _, l := range links {
		if l.To == ref {
			in = append(in, l)
		}
	}
	if w.reader.in == nil {
		w.reader.in = map[domain.NodeRef][]domain.Link{}
	}
	w.reader.in[ref] = in
	return nil
}

// enteringOf reads the stored links of some types (nil: every type) entering some nodes from the versions of their
// sources the world holds, by target: the sources first, then the links their versions hold into the nodes, so a
// source with a long history costs one version, not one row per version holding the link. A source the world holds as
// a draft is left to the drafts' links.
func (w *world) enteringOf(ctx context.Context, tx Tx, ids []domain.NodeID, types []string) (map[domain.NodeID][]domain.Link, error) {
	plan, err := w.enteringPlan(ctx, tx)
	if err != nil {
		return nil, err
	}
	if plan.sel != nil {
		links, err := plan.sel.LinksEnteringAt(ctx, w.namespace, plan.levels, ids, types, plan.skip)
		if err != nil {
			return nil, err
		}
		out := map[domain.NodeID][]domain.Link{}
		for _, l := range links {
			if v, ok := w.over[l.From.ID]; ok && v != l.From.Version {
				// the change holds another version than its branch: read as at does
				w.entering.sel = nil
				return w.enteringOf(ctx, tx, ids, types)
			}
			out[l.To.ID] = append(out[l.To.ID], l)
		}
		return out, nil
	}
	pairs, err := tx.LinkSources(ctx, ids, types, nil)
	if err != nil || len(pairs) == 0 {
		return map[domain.NodeID][]domain.Link{}, err
	}
	sources := make([]domain.NodeID, len(pairs))
	for i, p := range pairs {
		sources[i] = p.From
	}
	slices.Sort(sources)
	sources = slices.Compact(sources)
	at, err := w.at(ctx, tx, sources)
	if err != nil {
		return nil, err
	}
	refs := make([]domain.NodeRef, 0, len(at))
	for _, id := range sources {
		if v := at[id]; v != 0 {
			refs = append(refs, domain.NodeRef{ID: id, Version: v})
		}
	}
	out := map[domain.NodeID][]domain.Link{}
	if len(refs) == 0 {
		return out, nil
	}
	links, err := tx.LinksFromInto(ctx, refs, ids, types)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		out[l.To.ID] = append(out[l.To.ID], l)
	}
	return out, nil
}

// enteringPage reads the neighbourhood a node has through the links of some types (nil: every type) entering it from a
// page of their sources (by source, after page.After): a node with many entering links (a hub) is read a page at a
// time, as far as its reader goes. The drafts' links into the node come with the first page. It answers the last
// source of the page and whether there are more.
func (w *world) enteringPage(ctx context.Context, tx Tx, centre domain.Node, types []string, page sourcePage) (Neighbourhood, domain.NodeID, bool, error) {
	ref := centre.Ref()
	links, last, more, err := w.enteringLinksPage(ctx, tx, ref.ID, types, page)
	if err != nil {
		return Neighbourhood{}, "", false, err
	}
	if page.After == "" && (types == nil || len(types) > 0) {
		for _, d := range w.drafts() {
			dl, err := w.reader.outLinks(ctx, tx, domain.NodeRef{ID: d})
			if err != nil {
				return Neighbourhood{}, "", false, err
			}
			for _, l := range dl {
				if !l.External() && l.To.ID == ref.ID && (types == nil || slices.Contains(types, l.Type)) {
					links = append(links, l)
				}
			}
		}
	}
	held := map[domain.NodeID]domain.NodeRef{ref.ID: ref}
	out := map[domain.NodeID]Neighbourhood{ref.ID: {Node: centre}}
	nbs, err := w.complete(ctx, tx, []domain.NodeID{ref.ID}, held, out, nil, map[domain.NodeID][]domain.Link{ref.ID: links}, entering)
	return nbs[ref.ID], last, more, err
}

// enteringLinksPage reads the stored links of some types entering a node from a page of their sources (enteringOf).
func (w *world) enteringLinksPage(ctx context.Context, tx Tx, id domain.NodeID, types []string, page sourcePage) ([]domain.Link, domain.NodeID, bool, error) {
	if types != nil && len(types) == 0 {
		return nil, "", false, nil
	}
	plan, err := w.enteringPlan(ctx, tx)
	if err != nil {
		return nil, "", false, err
	}
	if plan.sel != nil {
		links, last, more, err := plan.sel.LinksEnteringPage(ctx, w.namespace, plan.levels, id, types, plan.skip, page)
		if err != nil {
			return nil, "", false, err
		}
		if !slices.ContainsFunc(links, func(l domain.Link) bool { v, ok := w.over[l.From.ID]; return ok && v != l.From.Version }) {
			return links, last, more, nil
		}
		// the change holds another version than its branch: read as at does
	}
	pairs, err := tx.LinkSources(ctx, []domain.NodeID{id}, types, &page)
	if err != nil || len(pairs) == 0 {
		return nil, "", false, err
	}
	more := len(pairs) > page.Limit
	pairs = pairs[:min(len(pairs), page.Limit)]
	sources := make([]domain.NodeID, len(pairs))
	for i, p := range pairs {
		sources[i] = p.From
	}
	at, err := w.at(ctx, tx, sources)
	if err != nil {
		return nil, "", false, err
	}
	var refs []domain.NodeRef
	for _, s := range sources {
		if v := at[s]; v != 0 {
			refs = append(refs, domain.NodeRef{ID: s, Version: v})
		}
	}
	var links []domain.Link
	if len(refs) > 0 {
		if links, err = tx.LinksFromInto(ctx, refs, []domain.NodeID{id}, types); err != nil {
			return nil, "", false, err
		}
	}
	return links, sources[len(sources)-1], more, nil
}

// enteringSelector is a store reading the links entering some nodes from the versions of their sources a state holds
// in one statement (the SQL ones): the sources, their selection (ADR 0143, its levels) and the links those versions
// hold, the sources skip left out.
type enteringSelector interface {
	LinksEnteringAt(ctx context.Context, namespace string, levels []selectLevel, ids []domain.NodeID, types []string, skip []domain.NodeID) ([]domain.Link, error)
	// LinksEnteringPage reads them for one node from a page of its sources: the last source of the page, and whether
	// there are more
	LinksEnteringPage(ctx context.Context, namespace string, levels []selectLevel, id domain.NodeID, types []string, skip []domain.NodeID,
		page sourcePage) ([]domain.Link, domain.NodeID, bool, error)
}

// maxSelectLevels bounds the levels of a selection read in one statement: main, a branch forked from it, the branch of
// a change over either.
const maxSelectLevels = 3

// enteringPlan is how a world reads the links entering its nodes: in one statement (sel set) when the store reads them
// so and the selection of the world is short (levels: the commit of a branch view, the fork of a change and, for a
// change that committed, its branch first), the sources the change holds a draft of or deleted left out (the drafts'
// links stand for the former); else sources, versions, links (three statements). Over its own branch, a change view
// reads the versions it wrote from the branch, as at does.
type enteringPlan struct {
	sel    enteringSelector
	levels []selectLevel
	skip   []domain.NodeID
}

func (w *world) enteringPlan(ctx context.Context, tx Tx) (*enteringPlan, error) {
	if w.entering != nil {
		return w.entering, nil
	}
	w.entering = &enteringPlan{}
	gt, ok := unwrapGuard(tx)
	if !ok {
		return w.entering, nil
	}
	sel, ok := gt.Tx.(enteringSelector)
	if !ok {
		return w.entering, nil
	}
	var levels []selectLevel
	var skip []domain.NodeID
	from := w.commit
	if w.change != nil {
		skip = w.drafts()
		for id := range w.gone {
			skip = append(skip, id)
		}
		if w.own {
			levels, from = append(levels, selectLevel{Branch: w.branch.Name, UpTo: math.MaxInt64}), w.branch.ForkCommit
		}
	}
	// the chain of the commits the state is read over (selectState): each level its branch up to its order
	for from != "" {
		if len(levels) == maxSelectLevels || gt.newCommits[from] {
			return w.entering, nil
		}
		h, err := gt.header(ctx, from)
		if err != nil {
			return nil, err
		}
		levels, from = append(levels, selectLevel{Branch: h.Branch, UpTo: h.Seq}), h.Fork
	}
	if len(levels) > 0 {
		*w.entering = enteringPlan{sel: sel, levels: levels, skip: skip}
	}
	return w.entering, nil
}

// ViewNodes returns a page of the nodes of a view ordered by key, matching q (ADR 0157): for a branch view the page of
// its commit (CommitNodes); for a change view the page of its fork with what the change wrote laid over it (its
// commits, its drafts, the nodes it created), read for those nodes only.
func (g *Graph) ViewNodes(ctx context.Context, v View, q NodeQuery) (page NodePage, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return page, err
	}
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil {
			return err
		}
		page, err = w.page(ctx, tx, q)
		page.Commit = w.commit
		return err
	})
	return page, err
}

// ViewNodesOfType returns the nodes of a type (that type exactly, not its subtypes; empty: every node) a view holds
// (ADR 0157): an explicit read of a whole type, what a script's ctx.nodes, a prompt template or an existence check asks
// for. One transaction and one read of the state, however many nodes come back.
func (g *Graph) ViewNodesOfType(ctx context.Context, v View, typ string) (out []domain.Node, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return nil, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil {
			return err
		}
		page, err := w.page(ctx, tx, NodeQuery{Type: typ, Limit: math.MaxInt32})
		if err != nil {
			return err
		}
		for _, n := range page.Nodes {
			if typ == "" || n.Type == typ {
				out = append(out, n)
			}
		}
		return nil
	})
	return out, err
}

// page is a page of the nodes of the world the caller reads.
func (w *world) page(ctx context.Context, tx Tx, q NodeQuery) (NodePage, error) {
	q.Reach = w.filter
	if w.change != nil {
		return w.nodes(ctx, tx, q)
	}
	if q.Change != "" {
		nodes, err := changeNodesIn(ctx, tx, w.commit, q.Change)
		return pageNodes(nodes, q), err
	}
	if w.commit == "" {
		return NodePage{}, nil
	}
	b, err := tx.Commit(ctx, w.commit)
	if err != nil || len(b.Nodes) == 0 {
		return NodePage{}, err
	}
	return tx.NodePage(ctx, refsOf(b.Nodes), q)
}

// typeBatch is the number of nodes of a type ofType selects at a time.
const typeBatch = 500

// ofType reads the nodes of a type a branch view holds whose key, type or a string property contains text, by id, at
// most limit (more: the view holds others): the node rows of the type a batch at a time (index node_type), each batch
// selected at the commit and read through the page of the store (its text and reach filters). Its cost follows the
// nodes read, never the size of the state, which a page reads whole. A change view is read by its page (ok false).
func (w *world) ofType(ctx context.Context, tx Tx, typ, text string, limit int) (nodes []domain.Node, more, ok bool, err error) {
	if w.change != nil {
		return nil, false, false, nil
	}
	if w.commit == "" || limit <= 0 {
		return nil, false, true, nil
	}
	var after domain.NodeID
	for {
		ids, err := tx.NodeIDsOfType(ctx, w.namespace, typ, after, typeBatch)
		if err != nil || len(ids) == 0 {
			return nodes, false, true, err
		}
		after = ids[len(ids)-1]
		at, err := versionsAt(ctx, tx, w.commit, ids)
		if err != nil {
			return nil, false, true, err
		}
		if len(at) > 0 {
			page, err := tx.NodePage(ctx, refsOf(at), NodeQuery{Type: typ, Text: text, Reach: w.filter, Limit: limit - len(nodes) + 1})
			if err != nil {
				return nil, false, true, err
			}
			for _, n := range page.Nodes {
				if len(nodes) == limit {
					return nodes, true, true, nil
				}
				nodes = append(nodes, n)
			}
		}
		if len(ids) < typeBatch {
			return nodes, false, true, nil
		}
	}
}

// nodes is the page of a change view: the fork's nodes the change left alone, paged by the store, merged with the
// nodes the change holds another version of (read node by node).
func (w *world) nodes(ctx context.Context, tx Tx, q NodeQuery) (NodePage, error) {
	if q.Change != "" { // the nodes the impacts of a change act on, as the fork has them
		nodes, err := changeNodesIn(ctx, tx, w.commit, q.Change)
		return pageNodes(nodes, q), err
	}
	// the nodes of the world the change wrote: their versions in it
	var mine []domain.NodeID
	if w.own {
		ids, err := tx.BranchNodeIDs(ctx, w.branch.Name, "")
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
		if w.sees(n) {
			written = append(written, n)
		}
	}

	// the fork, the nodes the change wrote left out, paged with room for them
	var page NodePage
	if w.commit != "" {
		b, err := tx.Commit(ctx, w.commit)
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
	extra := pageNodes(written, NodeQuery{Type: q.Type, Text: q.Text, IncludeDeleted: q.IncludeDeleted, Limit: max(q.Offset+q.Limit, MaxBrowseLimit)})
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

// ViewVersions answers what a view holds of some nodes (ADR 0157): their versions in it, read by node, and whether it
// is the head of main. The index keeps the hits of a search a view holds with it.
func (g *Graph) ViewVersions(ctx context.Context, v View, ids []domain.NodeID) (st ViewState, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return st, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil {
			return err
		}
		st.Namespace, st.Commit = w.namespace, w.commit
		if w.change == nil && domain.BranchOf(v.Branch) == domain.MainBranch {
			if v.Commit == "" {
				st.MainHead = true
			} else if head, err := branchHeadHeader(ctx, tx, w.namespace, domain.MainBranch); err != nil {
				return err
			} else {
				st.MainHead = head.ID == v.Commit
			}
		}
		st.Versions, err = w.at(ctx, tx, ids)
		return err
	})
	return st, err
}

// ViewDrafts returns the drafts a change view holds whose key, type or a string property contains the text
// (case-insensitive; empty: all), at most limit, by key: the nodes the index has no document of yet (ADR 0157). A branch
// view holds none.
func (g *Graph) ViewDrafts(ctx context.Context, v View, text string, limit int) (out []domain.Node, err error) {
	reach, err := g.reachOf(ctx)
	if err != nil {
		return nil, err
	}
	text = strings.ToLower(strings.TrimSpace(text))
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.openWorld(ctx, tx, v, reach)
		if err != nil || w.change == nil {
			return err
		}
		for _, id := range w.drafts() {
			n, err := w.node(ctx, tx, domain.NodeRef{ID: id})
			if err != nil {
				return err
			}
			if text == "" || nodeMatches(n, text) {
				out = append(out, n)
			}
		}
		return nil
	})
	slices.SortFunc(out, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, err
}
