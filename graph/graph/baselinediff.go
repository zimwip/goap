package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/zimwip/goap/contracts/domain"
)

// Kinds of a baseline difference.
const (
	DiffAdded   = "added"
	DiffRemoved = "removed"
	DiffChanged = "changed"
)

// BaselineDiff is a node whose version differs between two baselines: added (only in the second), removed (only in
// the first) or changed (another version). From / To are the node as each baseline holds it (nil where absent).
type BaselineDiff struct {
	Node domain.NodeID
	Key  string
	Type string
	Kind string
	From *domain.Node
	To   *domain.Node
}

// DiffBaselines compares two baselines of a namespace node by node: what going from the first to the second
// changes. Nodes are ordered by key. When the second follows the first on one branch (its head moving forward), only
// the nodes landed between them are read (ADR 0140); otherwise both states are read whole.
func (g *Graph) DiffBaselines(ctx context.Context, from, to domain.BaselineID) (out []BaselineDiff, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		ha, err := headerOf(ctx, tx, from)
		if err != nil {
			return err
		}
		hb, err := headerOf(ctx, tx, to)
		if err != nil {
			return err
		}
		if from != "" && to != "" && domain.NamespaceOf(ha.Namespace) != domain.NamespaceOf(hb.Namespace) {
			return fmt.Errorf("baselines %s and %s are of namespaces %s and %s: %w", from, to, ha.Namespace, hb.Namespace, ErrInvalid)
		}
		var a, b map[domain.NodeID]domain.Version
		ids, ok, err := landedBetween(ctx, tx, ha, hb)
		if err != nil {
			return err
		}
		if ok {
			if a, err = versionsAt(ctx, tx, from, ids); err != nil {
				return err
			}
			if b, err = versionsAt(ctx, tx, to, ids); err != nil {
				return err
			}
		} else {
			if a, err = baselineState(ctx, tx, from); err != nil {
				return err
			}
			if b, err = baselineState(ctx, tx, to); err != nil {
				return err
			}
		}
		// the versions on both sides, read in one query
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
			return err
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
			d := BaselineDiff{Node: id, Kind: DiffRemoved}
			if d.From, err = node(id, va); err != nil {
				return err
			}
			if ok {
				d.Kind = DiffChanged
				if d.To, err = node(id, vb); err != nil {
					return err
				}
			}
			out = append(out, d)
		}
		for id, vb := range b {
			if _, ok := a[id]; ok {
				continue
			}
			d := BaselineDiff{Node: id, Kind: DiffAdded}
			if d.To, err = node(id, vb); err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	for i := range out {
		n := out[i].To
		if n == nil {
			n = out[i].From
		}
		out[i].Key, out[i].Type = n.Key, n.Type
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

// maxLandedWalk bounds the baselines landedBetween walks from the second baseline back to the first.
const maxLandedWalk = 256

// landedBetween lists the nodes that may differ between baselines a and b when b follows a on one branch through
// baselines derived from their landings (a commit, a merge, a fast-forward, ADR 0140): the nodes landed on that branch
// after a, up to b. ok is false otherwise (another branch, a stored baseline between them, too far apart).
func landedBetween(ctx context.Context, tx Tx, a, b domain.Baseline) (ids []domain.NodeID, ok bool, err error) {
	if a.ID == "" || b.ID == "" || domain.BranchOf(a.Branch) != domain.BranchOf(b.Branch) || a.Seq >= b.Seq {
		return nil, a.ID == b.ID && a.ID != "", nil
	}
	cur := b
	for range maxLandedWalk {
		if !domain.DerivedKind(cur.Kind) || domain.BranchOf(cur.Branch) != domain.BranchOf(a.Branch) {
			return nil, false, nil
		}
		if cur.ParentID == a.ID {
			landed, err := tx.LandedVersions(ctx, b.Namespace, domain.BranchOf(b.Branch), a.Seq, b.Seq, nil)
			if err != nil {
				return nil, false, err
			}
			return slices.Collect(maps.Keys(landed)), true, nil
		}
		if cur.ParentID == "" {
			return nil, false, nil
		}
		if cur, err = cachedHeader(ctx, tx, cur.ParentID); err != nil {
			return nil, false, err
		}
	}
	return nil, false, nil
}

// baselineState reads the whole state of a baseline ("": the empty state).
func baselineState(ctx context.Context, tx Tx, id domain.BaselineID) (map[domain.NodeID]domain.Version, error) {
	if id == "" {
		return nil, nil
	}
	b, err := tx.Baseline(ctx, id)
	return b.Nodes, err
}
