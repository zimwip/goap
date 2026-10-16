package graphapi

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
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
// reentrant) and to the exact node versions the resulting baseline will hold.
type ValidatorQuery interface {
	// NodesOfType returns the live, non-deleted nodes of a type in a namespace, as the change leaves them.
	NodesOfType(ctx context.Context, namespace, typ string) ([]domain.Node, error)
	// OutLinksOf returns the outgoing links of a node version as the change leaves it.
	OutLinksOf(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error)
	// Node resolves a node: an exact version if ref.Version is set, else the version the change leaves it at.
	Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error)
}
