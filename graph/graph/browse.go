package graph

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// Browsing a baseline without shipping it whole: the IDE lists the node types of a baseline, pages through the
// nodes of one type with a text filter, and shows the neighbourhood of a selected node.

// MaxBrowseLimit caps the page size of BaselineNodes.
const MaxBrowseLimit = graphapi.MaxBrowseLimit

// The shapes of a browse belong to the graph's contract (ADR 0157: the port reads a view a page at a time).
type (
	NodeQuery     = graphapi.NodeQuery
	TypeCount     = graphapi.TypeCount
	NodePage      = graphapi.NodePage
	Neighbourhood = graphapi.Neighbourhood
)

// BaselineNodes returns a page of the nodes of a baseline, ordered by key: the store filters, counts and reads the page
// over the versions of the state (ADR 0140); with q.Change, the nodes of that change only.
func (g *Graph) BaselineNodes(ctx context.Context, id domain.BaselineID, q NodeQuery) (page NodePage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	reach, err := g.reachOf(ctx)
	if err != nil {
		return page, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		h, err := tx.BaselineHeader(ctx, id)
		if err != nil {
			return err
		}
		q.Reach = g.filterOf(reach.in(h.Namespace))
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
		if (n.Deleted && !q.IncludeDeleted) || (text != "" && !nodeMatches(n, text)) || !q.Reach.Holds(n) {
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
		if cn.Committed != nil {
			ids = append(ids, cn.Committed.ID)
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
	Text string
	// Reach keeps the links both ends of which are versions the caller reads (nil: every link, ADR 0161 §5).
	Reach         *graphapi.ReachFilter
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
	reach, err := g.reachOf(ctx)
	if err != nil {
		return page, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil || len(b.Nodes) == 0 {
			return err
		}
		q.Reach = g.filterOf(reach.in(b.Namespace))
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

// NodeNeighbourhood returns a node of a baseline with its direct neighbours; it reads the versions of the node and of
// its neighbours in the baseline, never its whole state (ADR 0137).
func (g *Graph) NodeNeighbourhood(ctx context.Context, id domain.BaselineID, nodeID domain.NodeID) (Neighbourhood, error) {
	b, err := g.BaselineHeader(ctx, id)
	if err != nil {
		return Neighbourhood{}, err
	}
	return g.ViewNeighbourhood(ctx, View{Namespace: b.Namespace, Branch: b.Branch, Baseline: id}, nodeID)
}

// ChangeNodeNeighbourhood returns a node and its direct neighbours as a change sees them (its world, ChangeWorld): the
// version of its fork the change left alone, the version it committed, or its draft (version 0), on the flow named
// (else the active option, else the main flow). Like NodeNeighbourhood it reads the versions of the node and of its
// neighbours only, never the whole world: the cost follows the node, not the namespace (ADR 0137). A link is kept when
// its other end is in the world, at whatever version it names (the world is read by node).
func (g *Graph) ChangeNodeNeighbourhood(ctx context.Context, id domain.ChangeID, flow string, nodeID domain.NodeID) (Neighbourhood, error) {
	return g.ViewNeighbourhood(ctx, View{Change: id, Flow: flow}, nodeID)
}

// Namespaces returns the namespaces holding at least one node.
func (g *Graph) Namespaces(ctx context.Context) (ns []string, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { ns, err = tx.Namespaces(ctx); return err })
	return
}
