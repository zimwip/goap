package graphsvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// ProjectMoveGate is the gate of a move of changes to another project (ADR 0091), plugged by the compositions that
// hold the organisation (cmd/graph, goap-dev): the caller needs change:move on the project of each change and on
// the target, and access to both (access.Snapshot.MayAccessProject); and the methodology of each change must apply
// (its own or inherited, Snapshot.ApplicableMethodologies) to both. A change with no methodology is not held to the
// second rule. A platform service (authz.Principal.System) is not asked the first two. Either argument may be nil:
// the check it does is then skipped.
func ProjectMoveGate(a authz.Authorizer, d *access.Directory) graph.ProjectMoveGate {
	return func(ctx context.Context, family []domain.Change, to string) error {
		who := authz.From(ctx)
		var snap *access.Snapshot
		if d != nil {
			var err error
			if snap, err = d.Snapshot(ctx); err != nil {
				return fmt.Errorf("the organisation cannot be read: %w", err)
			}
		}
		for _, c := range family {
			if !who.System() {
				for _, project := range []string{c.ProjectID, to} {
					if snap != nil && !snap.MayAccessProject(who, project) {
						return fmt.Errorf("you may not work on project %s: %w", project, authz.ErrForbidden)
					}
					if a != nil {
						if err := authz.Check(ctx, a, authz.Request{Subject: who, Action: "move",
							Resource: authz.Resource{Type: "change", ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: project}}); err != nil {
							return fmt.Errorf("moving change %s on project %s: %w", c.ID, project, err)
						}
					}
				}
			}
			if snap == nil || c.Methodology == "" {
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
}
