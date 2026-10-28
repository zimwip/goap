package graph

import (
	"context"
	"fmt"
	"sort"

	"github.com/zimwip/goap/contracts/domain"
)

// Kinds of a commit difference.
const (
	DiffAdded   = "added"
	DiffRemoved = "removed"
	DiffChanged = "changed"
)

// CommitDiff is a node whose version differs between two commits: added (only in the second), removed (only in
// the first) or changed (another version). From / To are the node as each commit holds it (nil where absent).
type CommitDiff struct {
	Node domain.NodeID
	Key  string
	Type string
	Kind string
	From *domain.Node
	To   *domain.Node
}

// DiffCommits compares two commits of a namespace node by node: what going from the first to the second
// changes. Nodes are ordered by key.
func (g *Graph) DiffCommits(ctx context.Context, from, to domain.CommitID) (out []CommitDiff, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) (err error) { out, err = diffCommits(ctx, tx, from, to); return })
	return
}

// diffCommits is DiffCommits in a transaction, one way whatever the two commits (ADR 0140): the changes only one
// of them takes in (lineageGap) name the nodes the states may differ on, the nodes of the versions they wrote and of
// their own branches; the versions of those nodes alone are read in each commit, then compared.
func diffCommits(ctx context.Context, tx Tx, from, to domain.CommitID) ([]CommitDiff, error) {
	gap, err := lineageGap(ctx, tx, from, to)
	if err != nil {
		return nil, err
	}
	var ids []domain.NodeID
	seen := map[domain.NodeID]bool{}
	add := func(id domain.NodeID) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	heads, err := changeHeads(ctx, tx, gap.changes)
	if err != nil {
		return nil, err
	}
	for _, id := range gap.changes {
		c, ok := heads[id]
		if !ok {
			continue
		}
		// the versions it wrote (ADR 0174)
		rows, err := tx.CommitImpacts(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			add(r.Node)
		}
		if c.Branch != "" {
			// what its branch holds: a node it reverted keeps no impact
			on, err := tx.BranchNodeIDs(ctx, c.Branch)
			if err != nil {
				return nil, err
			}
			for _, id := range on {
				add(id)
			}
		}
	}
	a, err := versionsAt(ctx, tx, from, ids)
	if err != nil {
		return nil, err
	}
	b, err := versionsAt(ctx, tx, to, ids)
	if err != nil {
		return nil, err
	}
	return diffStates(ctx, tx, a, b)
}

// diffStates compares two states over the same nodes, reading the versions on both sides in one query.
func diffStates(ctx context.Context, tx Tx, a, b map[domain.NodeID]domain.Version) (out []CommitDiff, err error) {
	var refs []domain.NodeRef
	for id, va := range a {
		if vb, ok := b[id]; !ok || va != vb {
			refs = append(refs, domain.NodeRef{ID: id, Version: va})
			if ok {
				refs = append(refs, domain.NodeRef{ID: id, Version: vb})
			}
		}
	}
	for id, vb := range b {
		if _, ok := a[id]; !ok {
			refs = append(refs, domain.NodeRef{ID: id, Version: vb})
		}
	}
	read, err := tx.NodesAt(ctx, refs, "")
	if err != nil {
		return nil, err
	}
	nodes := make(map[domain.NodeRef]*domain.Node, len(read))
	for i := range read {
		nodes[read[i].Ref()] = &read[i]
	}
	node := func(id domain.NodeID, v domain.Version) (*domain.Node, error) {
		if n, ok := nodes[domain.NodeRef{ID: id, Version: v}]; ok {
			return n, nil
		}
		return nil, fmt.Errorf("node %s v%d: %w", id, v, ErrNotFound)
	}
	for id, va := range a {
		vb, ok := b[id]
		if ok && va == vb {
			continue
		}
		d := CommitDiff{Node: id, Kind: DiffRemoved}
		if d.From, err = node(id, va); err != nil {
			return nil, err
		}
		if ok {
			d.Kind = DiffChanged
			if d.To, err = node(id, vb); err != nil {
				return nil, err
			}
		}
		out = append(out, d)
	}
	for id, vb := range b {
		if _, ok := a[id]; ok {
			continue
		}
		d := CommitDiff{Node: id, Kind: DiffAdded}
		if d.To, err = node(id, vb); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	for i := range out {
		n := out[i].To
		if n == nil {
			n = out[i].From
		}
		out[i].Key, out[i].Type = n.Key, n.Type
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
