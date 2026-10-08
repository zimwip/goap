package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Guardian is what a change asks before it lands, takes a sub-change or moves to another project (ADR 0098): the rules
// of whoever governs the change (its methodology), which a manual operation on the change must not skip. The graph
// does not know what a guardian checks; it resolves the one the change names (Change.Guardian) among Graph.Guardians
// and refuses the operation when the change names one it cannot reach. Every method runs outside any transaction and
// may read the graph.
type Guardian interface {
	// MayCommit decides whether c lands, on the blackboard its landing builds (the change and its impacts hydrated with
	// the versions it writes): decided replaces the floor of the landable states (ADR 0078) by ok; not decided leaves
	// the floor in force.
	MayCommit(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error)
	// MayCreateChild accepts or refuses a sub-change of parent, before it is stored.
	MayCreateChild(ctx context.Context, parent, child domain.Change) error
	// MayMove accepts or refuses the move of a family of changes (a root change and its open sub-changes) to project to
	// (ADR 0091), after the move was authorized (Graph.ProjectMoveGate).
	MayMove(ctx context.Context, family []domain.Change, to string) error
}

// guardianOf resolves the guardian a change names: nil for a free change; an error (ErrConflict) for a name the graph
// has no guardian of: the change is held by rules nothing can check now, and the operation is refused.
func (g *Graph) guardianOf(c domain.Change) (Guardian, error) {
	if c.Guardian == "" {
		return nil, nil
	}
	if gd, ok := g.Guardians[c.Guardian]; ok && gd != nil {
		return gd, nil
	}
	return nil, fmt.Errorf("change %s is guarded by %s, which is not available: %w", c.ID, c.Guardian, ErrConflict)
}
