package registrysvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// GuardianName is the name of the guardian of the registry on the graph (ADR 0098): the rules of the methodology of a
// change, until the engine holds them (phase 5 of the ADR).
const GuardianName = "registry"

// Guardian is the guardian (graph.Guardian, ADR 0098) of the changes governed by a methodology the registry holds: the
// goal of the Activity a change is scoped to at its landing (LandingGate), the sub_activity cascade of its sub-changes
// (SubChangeValidator), and a move only between projects that both apply its methodology. Directory reads the
// organisation for the last rule; nil: not checked.
type Guardian struct {
	Service   *Service
	Directory *access.Directory
}

var _ graph.Guardian = Guardian{}

// MayCommit implements graph.Guardian.
func (gd Guardian) MayCommit(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error) {
	return gd.Service.LandingGate(ctx, c, bb)
}

// MayCreateChild implements graph.Guardian.
func (gd Guardian) MayCreateChild(ctx context.Context, parent, child domain.Change) error {
	return gd.Service.SubChangeValidator(ctx, parent, child)
}

// MayMove implements graph.Guardian: the methodology of each change of the family applies (its own or inherited,
// access.Snapshot.ApplicableMethodologies) to the project it leaves and to the one it goes to. A change with no
// methodology is not held to it.
func (gd Guardian) MayMove(ctx context.Context, family []domain.Change, to string) error {
	if gd.Directory == nil {
		return nil
	}
	snap, err := gd.Directory.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("the organisation cannot be read: %w", err)
	}
	for _, c := range family {
		if c.Methodology == "" {
			continue
		}
		for _, project := range []string{c.ProjectID, to} {
			if !slices.Contains(snap.ApplicableMethodologies(project), c.Methodology) {
				return fmt.Errorf("methodology %s of change %s does not apply to project %s (a change moves between projects that both apply its methodology): %w",
					c.Methodology, c.ID, project, graph.ErrInvalid)
			}
		}
	}
	return nil
}
