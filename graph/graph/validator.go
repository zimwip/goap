package graph

import (
	"context"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// txQuery implements ValidatorQuery against the resulting node set of a change being applied (target: the map
// Apply is about to persist as the new baseline's Nodes, ADR 0024), not a branch read: an impacted node's new
// version only joins its branch once Apply advances it, after validators run.
type txQuery struct {
	tx     Tx
	target map[domain.NodeID]domain.Version
}

func (q txQuery) NodesOfType(ctx context.Context, namespace, typ string) ([]domain.Node, error) {
	ns := domain.NamespaceOf(namespace)
	var out []domain.Node
	for id, v := range q.target {
		n, err := q.tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
		if err != nil {
			return nil, err
		}
		if n.Namespace == ns && n.Type == typ && !n.Deleted {
			out = append(out, n)
		}
	}
	return out, nil
}

func (q txQuery) OutLinksOf(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return q.tx.OutLinks(ctx, ref)
}

func (q txQuery) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	if ref.Version == 0 {
		ref.Version = q.target[ref.ID]
	}
	return q.tx.Node(ctx, ref)
}

// runNodeValidators runs the registered NodeValidator plugins whose declared types intersect the types this
// change actually wrote (ADR 0048), grouping cposts per validator so each sees only its own impacted nodes.
func (g *Graph) runNodeValidators(ctx context.Context, tx Tx, target map[domain.NodeID]domain.Version, cposts []cpost) error {
	if len(g.Validators) == 0 {
		return nil
	}
	q := txQuery{tx: tx, target: target}
	for _, v := range g.Validators {
		types := v.Types()
		var impacted []ValidatedNode
		for _, cp := range cposts {
			if !slices.Contains(types, cp.post.Type) {
				continue
			}
			impacted = append(impacted, ValidatedNode{Post: cp.post, Pre: cp.pre})
		}
		if len(impacted) == 0 {
			continue
		}
		if err := v.Validate(ctx, q, impacted); err != nil {
			return err
		}
	}
	return nil
}
