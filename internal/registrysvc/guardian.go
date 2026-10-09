package registrysvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// GuardianName names the rules of the registry when a composition makes them the guardian of its changes by themselves
// (tests); the platform's guardian is the engine's (engine.GuardianName), which asks these rules after its own.
const GuardianName = "registry"

// Guardian is the part of the guardian of a change (graph.Guardian, ADR 0098) that the registry holds, asked by the
// engine's guardian (engine.Guardian.Next) after the lifecycle: the goal of the Activity a change is scoped to at its
// landing (LandingGate), the sub_activity cascade of its sub-changes (SubChangeValidator), and a move only between
// projects that both apply its methodology. Directory reads the organisation for the last rule; nil: not checked.
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

// MayEdit implements graph.Guardian: the registry's rules freeze nothing (the lifecycle of a change is the engine's).
func (Guardian) MayEdit(context.Context, domain.Change, domain.ChangeImpactID) error { return nil }
