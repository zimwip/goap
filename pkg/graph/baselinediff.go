package graph

import (
	"context"
	"fmt"
	"sort"

	"github.com/zimwip/goap/pkg/domain"
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
// changes. Nodes are ordered by key.
func (g *Graph) DiffBaselines(ctx context.Context, from, to domain.BaselineID) (out []BaselineDiff, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		a, err := tx.Baseline(ctx, from)
		if err != nil {
			return err
		}
		b, err := tx.Baseline(ctx, to)
		if err != nil {
			return err
		}
		if domain.NamespaceOf(a.Namespace) != domain.NamespaceOf(b.Namespace) {
			return fmt.Errorf("baselines %s and %s are of namespaces %s and %s: %w", from, to, a.Namespace, b.Namespace, ErrInvalid)
		}
		node := func(id domain.NodeID, v domain.Version) (*domain.Node, error) {
			n, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
			if err != nil {
				return nil, err
			}
			return &n, nil
		}
		for id, va := range a.Nodes {
			vb, ok := b.Nodes[id]
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
		for id, vb := range b.Nodes {
			if _, ok := a.Nodes[id]; ok {
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
