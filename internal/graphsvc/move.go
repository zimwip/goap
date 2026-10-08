package graphsvc

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// ProjectMoveGate authorizes a move of changes to another project (ADR 0091), plugged by the compositions that hold the
// organisation (cmd/graph, goap-dev): the caller needs change:move on the project of each change and on the target, and
// access to both (access.Snapshot.MayAccessProject). A platform service (authz.Principal.System) is not asked. Either
// argument may be nil: the check it does is then skipped. Whether the methodology of a change applies to both projects
// is its guardian's (registrysvc.Guardian.MayMove, ADR 0098).
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
		}
		return nil
	}
}
