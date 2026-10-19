package graph

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// Browsing a baseline without shipping it whole: the IDE lists the node types of a baseline, pages through the
// nodes of one type with a text filter, and shows the neighbourhood of a selected node.

// MaxBrowseLimit caps the page size of BaselineNodes.
const MaxBrowseLimit = 500

// NodeQuery selects a page of the nodes of a baseline.
type NodeQuery struct {
	// Type keeps the nodes of this qualified type (empty: every type).
	Type string
	// Text keeps the nodes whose key, type or a string property contains it (case-insensitive).
	Text string
	// IncludeDeleted keeps the tombstones.
	IncludeDeleted bool
	// Change keeps the nodes the impacts of that change act on, at their version in the baseline: they are read node
	// by node, so the answer and its cost follow the change, not the size of the baseline.
	Change        domain.ChangeID
	Offset, Limit int
}

// TypeCount is the number of nodes of a type in a baseline.
type TypeCount struct {
	Type  string
	Count int
}

// NodePage is a page of the nodes of a baseline.
type NodePage struct {
	Nodes []domain.Node
	// Total is the number of nodes matching the query (all pages).
	Total int
	// Types counts the nodes of each type matching the text filter (the type filter aside), sorted by type.
	Types []TypeCount
}

// BaselineNodes returns a page of the nodes of a baseline, ordered by key: the store filters, counts and reads the page
// over the versions of the state (ADR 0140); with q.Change, the nodes of that change only.
func (g *Graph) BaselineNodes(ctx context.Context, id domain.BaselineID, q NodeQuery) (page NodePage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if q.Change != "" {
			nodes, err := changeNodesIn(ctx, tx, id, q.Change)
			page = pageNodes(nodes, q)
			return err
		}
		b, err := tx.Baseline(ctx, id)
		if err != nil || len(b.Nodes) == 0 {
			return err
		}
		page, err = tx.NodePage(ctx, refsOf(b.Nodes), q)
		return err
	})
	return page, err
}

// pageNodes is the page q asks of nodes ordered by key, filtered and counted in memory.
func pageNodes(nodes []domain.Node, q NodeQuery) (page NodePage) {
	text := strings.ToLower(strings.TrimSpace(q.Text))
	counts := map[string]int{}
	var matched []domain.Node
	for _, n := range nodes {
		if (n.Deleted && !q.IncludeDeleted) || (text != "" && !nodeMatches(n, text)) {
			continue
		}
		counts[n.Type]++
		if q.Type == "" || n.Type == q.Type {
			matched = append(matched, n)
		}
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	if q.Offset < len(matched) {
		page.Nodes = matched[q.Offset:min(q.Offset+q.Limit, len(matched))]
	}
	return page
}

// typeCounts sorts the counts by type, and the total of typ (every type when empty).
func typeCounts(counts map[string]int, typ string) (types []TypeCount, total int) {
	for t, c := range counts {
		types = append(types, TypeCount{Type: t, Count: c})
		if typ == "" || t == typ {
			total += c
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Type < types[j].Type })
	return types, total
}

// changeNodesIn reads, at baseline id, the nodes the impacts of a change act on, ordered by key (ADR 0137): never the
// whole state of the baseline.
func changeNodesIn(ctx context.Context, tx Tx, id domain.BaselineID, change domain.ChangeID) ([]domain.Node, error) {
	c, err := tx.Change(ctx, change)
	if err != nil {
		return nil, err
	}
	var ids []domain.NodeID
	for _, cn := range c.Nodes {
		if n := nodeOf(cn); n != "" {
			ids = append(ids, n)
		}
		if cn.Landed != nil {
			ids = append(ids, cn.Landed.ID)
		}
	}
	slices.Sort(ids)
	at, err := versionsAt(ctx, tx, id, slices.Compact(ids))
	if err != nil {
		return nil, err
	}
	refs := make([]domain.NodeRef, 0, len(at))
	for n, v := range at {
		refs = append(refs, domain.NodeRef{ID: n, Version: v})
	}
	nodes, err := tx.NodesAt(ctx, refs, "") // one statement, not one per node
	if err != nil {
		return nil, err
	}
	slices.SortFunc(nodes, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	return nodes, nil
}

// LinkQuery selects a page of the links of a baseline.
type LinkQuery struct {
	// Type keeps the links of this type (empty: every type).
	Type string
	// Text keeps the links whose type or a string property contains it (case-insensitive).
	Text          string
	Offset, Limit int
}

// LinkPage is a page of the links of a baseline.
type LinkPage struct {
	Links []domain.Link
	// Total is the number of links matching the query (all pages).
	Total int
	// Types counts the links of each type matching the text filter (the type filter aside), sorted by type.
	Types []TypeCount
}

// BaselineLinks returns a page of the links of a baseline (both ends held by it), ordered by type then id: the store
// filters, counts and reads the page over the versions of the state (ADR 0140).
func (g *Graph) BaselineLinks(ctx context.Context, id domain.BaselineID, q LinkQuery) (page LinkPage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil || len(b.Nodes) == 0 {
			return err
		}
		page, err = tx.LinkPage(ctx, refsOf(b.Nodes), q)
		return err
	})
	return page, err
}

// pageLinks is the page q asks of links, filtered, ordered and counted in memory.
func pageLinks(links []domain.Link, q LinkQuery) (page LinkPage) {
	links = slices.Clone(links)
	sort.Slice(links, func(i, j int) bool {
		if links[i].Type != links[j].Type {
			return links[i].Type < links[j].Type
		}
		return links[i].ID < links[j].ID
	})
	text := strings.ToLower(strings.TrimSpace(q.Text))
	counts := map[string]int{}
	var matched []domain.Link
	for _, l := range links {
		if text != "" && !linkMatches(l, text) {
			continue
		}
		counts[l.Type]++
		if q.Type == "" || l.Type == q.Type {
			matched = append(matched, l)
		}
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	if q.Offset < len(matched) {
		page.Links = matched[q.Offset:min(q.Offset+q.Limit, len(matched))]
	}
	return page
}

// linkMatches reports whether the type or a string property of l contains text (lower case).
func linkMatches(l domain.Link, text string) bool {
	if strings.Contains(strings.ToLower(l.Type), text) {
		return true
	}
	for _, v := range l.Properties {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), text) {
			return true
		}
	}
	return false
}

// nodeMatches reports whether the key, the type or a string property of n contains text (lower case).
func nodeMatches(n domain.Node, text string) bool {
	if strings.Contains(strings.ToLower(n.Key), text) || strings.Contains(strings.ToLower(n.Type), text) {
		return true
	}
	for _, v := range n.Properties {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), text) {
			return true
		}
	}
	return false
}

// Neighbourhood is a node of a baseline and the nodes it is linked to.
type Neighbourhood struct {
	Node domain.Node
	// Nodes are the neighbours: the targets of its outgoing links (at the baseline's version, or the linked one for a
	// node the baseline does not hold, such as a node of another namespace) and the sources of its incoming links
	// held by the baseline.
	Nodes []domain.Node
	// Links are the links between Node and its neighbours.
	Links []domain.Link
	// Suspect are the links whose target changed since they were created (the baseline holds another version).
	Suspect []domain.LinkID
}

// NodeNeighbourhood returns a node of a baseline with its direct neighbours; it reads the versions of the node and of
// its neighbours in the baseline, never its whole state (ADR 0137).
func (g *Graph) NodeNeighbourhood(ctx context.Context, id domain.BaselineID, nodeID domain.NodeID) (nb Neighbourhood, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.BaselineHeader(ctx, id); err != nil {
			return err
		}
		at, err := versionsAt(ctx, tx, id, []domain.NodeID{nodeID})
		if err != nil {
			return err
		}
		v, ok := at[nodeID]
		if !ok {
			return fmt.Errorf("node %s in baseline %s: %w", nodeID, id, ErrNotFound)
		}
		ref := domain.NodeRef{ID: nodeID, Version: v}
		if nb.Node, err = tx.Node(ctx, ref); err != nil {
			return err
		}
		// Outgoing links belong to the source version: all of them are the node's. Incoming links may target any
		// earlier version of the node (suspect links): keep those whose source is the baseline's version.
		out, err := tx.OutLinks(ctx, ref)
		if err != nil {
			return err
		}
		in, err := tx.LinksTo(ctx, nodeID)
		if err != nil {
			return err
		}
		var near []domain.NodeID
		for _, l := range out {
			near = append(near, l.To.ID)
		}
		for _, l := range in {
			near = append(near, l.From.ID)
		}
		slices.Sort(near)
		if at, err = versionsAt(ctx, tx, id, slices.Compact(near)); err != nil {
			return err
		}
		seen := map[domain.NodeRef]bool{ref: true}
		add := func(r domain.NodeRef) error {
			if seen[r] {
				return nil
			}
			seen[r] = true
			n, err := tx.Node(ctx, r)
			if err != nil {
				return err
			}
			nb.Nodes = append(nb.Nodes, n)
			return nil
		}
		for _, l := range out {
			to := l.To
			if tv, ok := at[to.ID]; ok && tv != to.Version {
				to.Version = tv
				nb.Suspect = append(nb.Suspect, l.ID)
			}
			if err := add(to); err != nil {
				return err
			}
			nb.Links = append(nb.Links, l)
		}
		slices.SortStableFunc(in, func(a, b domain.Link) int { return int(a.To.Version) - int(b.To.Version) })
		for _, l := range in {
			if l.To.Version > v || l.From.ID == nodeID || at[l.From.ID] != l.From.Version { // a self link is already among the outgoing ones
				continue
			}
			if err := add(l.From); err != nil {
				return err
			}
			if l.To.Version != v {
				nb.Suspect = append(nb.Suspect, l.ID)
			}
			nb.Links = append(nb.Links, l)
		}
		return nil
	})
	return
}

// Namespaces returns the namespaces holding at least one node.
func (g *Graph) Namespaces(ctx context.Context) (ns []string, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { ns, err = tx.Namespaces(ctx); return err })
	return
}
