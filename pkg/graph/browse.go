package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
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
	Offset, Limit  int
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

// BaselineNodes returns a page of the nodes of a baseline, ordered by key.
func (g *Graph) BaselineNodes(ctx context.Context, id domain.BaselineID, q NodeQuery) (page NodePage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	text := strings.ToLower(strings.TrimSpace(q.Text))
	var nodes []domain.Node
	if err = g.repo.InTx(ctx, func(tx Tx) (err error) { nodes, err = tx.NodesIn(ctx, id, ""); return }); err != nil {
		return page, err
	}
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
	for t, c := range counts {
		page.Types = append(page.Types, TypeCount{Type: t, Count: c})
	}
	sort.Slice(page.Types, func(i, j int) bool { return page.Types[i].Type < page.Types[j].Type })
	page.Total = len(matched)
	if q.Offset < len(matched) {
		page.Nodes = matched[q.Offset:min(q.Offset+q.Limit, len(matched))]
	}
	return page, nil
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

// BaselineLinks returns a page of the links of a baseline (both ends held by it), ordered by type then id.
func (g *Graph) BaselineLinks(ctx context.Context, id domain.BaselineID, q LinkQuery) (page LinkPage, err error) {
	if q.Limit <= 0 || q.Limit > MaxBrowseLimit {
		q.Limit = MaxBrowseLimit
	}
	q.Offset = max(q.Offset, 0)
	text := strings.ToLower(strings.TrimSpace(q.Text))
	var links []domain.Link
	if err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		b, err := tx.Baseline(ctx, id)
		if err != nil {
			return err
		}
		nodes, err := tx.NodesIn(ctx, id, "")
		if err != nil {
			return err
		}
		links, err = linksWithin(ctx, tx, b, nodes)
		return err
	}); err != nil {
		return page, err
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Type != links[j].Type {
			return links[i].Type < links[j].Type
		}
		return links[i].ID < links[j].ID
	})
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
	for t, c := range counts {
		page.Types = append(page.Types, TypeCount{Type: t, Count: c})
	}
	sort.Slice(page.Types, func(i, j int) bool { return page.Types[i].Type < page.Types[j].Type })
	page.Total = len(matched)
	if q.Offset < len(matched) {
		page.Links = matched[q.Offset:min(q.Offset+q.Limit, len(matched))]
	}
	return page, nil
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

// NodeNeighbourhood returns a node of a baseline with its direct neighbours.
func (g *Graph) NodeNeighbourhood(ctx context.Context, id domain.BaselineID, nodeID domain.NodeID) (nb Neighbourhood, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil {
			return err
		}
		v, ok := b.Nodes[nodeID]
		if !ok {
			return fmt.Errorf("node %s in baseline %s: %w", nodeID, id, ErrNotFound)
		}
		ref := domain.NodeRef{ID: nodeID, Version: v}
		if nb.Node, err = tx.Node(ctx, ref); err != nil {
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
		// Outgoing links belong to the source version: all of them are the node's.
		out, err := tx.OutLinks(ctx, ref)
		if err != nil {
			return err
		}
		for _, l := range out {
			to := l.To
			if tv, ok := b.Nodes[to.ID]; ok && tv != to.Version {
				to.Version = tv
				nb.Suspect = append(nb.Suspect, l.ID)
			}
			if err := add(to); err != nil {
				return err
			}
			nb.Links = append(nb.Links, l)
		}
		// Incoming links may target any earlier version of the node (suspect links): keep those whose source is
		// the baseline's version.
		for ver := domain.Version(1); ver <= v; ver++ {
			in, err := tx.InLinks(ctx, domain.NodeRef{ID: nodeID, Version: ver})
			if err != nil {
				return err
			}
			for _, l := range in {
				if l.From.ID == nodeID || !b.Contains(l.From) { // a self link is already among the outgoing ones
					continue
				}
				if err := add(l.From); err != nil {
					return err
				}
				if ver != v {
					nb.Suspect = append(nb.Suspect, l.ID)
				}
				nb.Links = append(nb.Links, l)
			}
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
