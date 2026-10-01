package graph

import (
	"context"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// NodeValidator checks a graph-wide invariant the resulting state of a change must uphold (ADR 0048). Unlike
// a property validator (ADR 0018: pure, single-node, no graph access, run by validateProps), a NodeValidator
// may query the rest of the graph as it will be once the change lands. It runs once per Apply, after every
// node write of the change is checked (checkChangeImpacts) and before the resulting baseline is persisted: an
// error here aborts the whole Apply, so the change never lands.
type NodeValidator interface {
	// Types names the node types whose creation, modification or retirement in a change should trigger this
	// validator: Validate is only called when the change's own impact list touches at least one of them.
	Types() []string
	// Validate checks the invariant. impacted is the subset of the change's own writes whose type is one of
	// Types(); q gives read access to the rest of the graph as it will be once the change lands.
	Validate(ctx context.Context, q ValidatorQuery, impacted []ValidatedNode) error
}

// ValidatedNode is one node version a change wrote, of a type a NodeValidator declared interest in.
type ValidatedNode struct {
	Post domain.Node  // the version the change produces
	Pre  *domain.Node // nil for a created node
}

// ValidatorQuery is the read access into the resulting state of the change being applied that a NodeValidator
// gets: bound to the transaction Apply already runs in (there is no nested transaction, the stores are not
// reentrant, see authorizeMoves) and to the exact node versions the resulting baseline will hold.
type ValidatorQuery interface {
	// NodesOfType returns the live, non-deleted nodes of a type in a namespace, as the change leaves them.
	NodesOfType(ctx context.Context, namespace, typ string) ([]domain.Node, error)
	// OutLinksOf returns the outgoing links of a node version as the change leaves it.
	OutLinksOf(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	// Node resolves a node: an exact version if ref.Version is set, else the version the change leaves it at.
	Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error)
}

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
