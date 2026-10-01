package access

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// AdminFloorValidator refuses a change that would leave the graph with no active administrator (ADR 0020's
// floor policy, enforced by ADR 0048's NodeValidator mechanism): "administrator" is a User holding the
// platform RoleAdmin through an Assignment (ADR 0047), not the legacy User.Admin flag; "active" is the user
// lifecycle's active state (ADR 0048). Wired onto Graph.Validators (see cmd/goap-dev, cmd/graph).
type AdminFloorValidator struct{}

// Types runs this validator whenever a change touches a User or an Assignment: either can remove the last
// administrator (deactivating the User, or retiring/narrowing their platform Assignment).
func (AdminFloorValidator) Types() []string {
	return []string{NodeTypeUser, NodeTypeAssignment}
}

func (AdminFloorValidator) Validate(ctx context.Context, q graph.ValidatorQuery, impacted []graph.ValidatedNode) error {
	users, err := q.NodesOfType(ctx, mcp.NamespaceOrganisation, NodeTypeUser)
	if err != nil {
		return err
	}
	assignments, err := q.NodesOfType(ctx, mcp.NamespaceOrganisation, NodeTypeAssignment)
	if err != nil {
		return err
	}
	admins := map[domain.NodeID]bool{} // User node ID -> holds RoleAdmin through a platform Assignment
	for _, asgNode := range assignments {
		asg, err := AssignmentFromProps(asgNode.Properties)
		if err != nil || !slices.Contains(asg.Roles, RoleAdmin) {
			continue
		}
		links, err := q.OutLinksOf(ctx, asgNode.Ref())
		if err != nil {
			return err
		}
		platform := true
		for _, l := range links {
			if l.Type == LinkAssignsProject {
				platform = false // project-scoped assignment, not a platform one (ADR 0046)
				break
			}
		}
		if !platform {
			continue
		}
		for _, l := range links {
			if l.Type == LinkAssignsOrg {
				admins[l.To.ID] = true
			}
		}
	}
	for _, u := range users {
		if u.State == "active" && admins[u.ID] {
			return nil // at least one active administrator remains
		}
	}
	return fmt.Errorf("this change would leave no active administrator: %w", graph.ErrInvalid)
}
