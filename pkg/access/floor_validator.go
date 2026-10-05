package access

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// AdminFloorValidator refuses a change that would leave the graph with no active administrator (ADR 0020's
// floor policy, enforced by ADR 0048's NodeValidator mechanism): "administrator" is a User holding the
// platform RoleAdmin as Snapshot.Enrich grants it (ADR 0047: an Assignment of the user or of a unit above it, ); "active" is the user lifecycle's active state (ADR 0048). Wired onto Graph.Validators (see cmd/goap-dev, cmd/graph).
type AdminFloorValidator struct{}

// Types runs this validator whenever a change touches a User or an Assignment: either can remove the last
// administrator (deactivating the User, or retiring/narrowing their platform Assignment).
func (AdminFloorValidator) Types() []string {
	return []string{NodeTypeUser, NodeTypeAssignment}
}

// Validate counts the active administrators the way Snapshot.Enrich grants the role, on the state the change
// leaves: the snapshot of that state is built and each active user enriched, so a
// platform Assignment held by a unit the user belongs to (or one of its ancestors) count as much as one held by the
// User node itself. Counting less would refuse a change that leaves an administrator; counting more, accept one
// that locks everybody out.
func (AdminFloorValidator) Validate(ctx context.Context, q graph.ValidatorQuery, impacted []graph.ValidatedNode) error {
	var nodes []domain.Node
	var links []domain.Link
	for _, typ := range []string{NodeTypeUser, NodeTypeAssignment, domain.TypeOrgUnit} {
		ns, err := q.NodesOfType(ctx, domain.NamespaceOrganisation, typ)
		if err != nil {
			return err
		}
		for _, n := range ns {
			ls, err := q.OutLinksOf(ctx, n.Ref())
			if err != nil {
				return err
			}
			nodes, links = append(nodes, n), append(links, ls...)
		}
	}
	snap := BuildSnapshot(domain.BuiltinStructureSet(), "", nodes, links)
	for _, n := range nodes {
		if n.Type != NodeTypeUser || n.State != "active" {
			continue
		}
		u, err := UserFromProps(n.Properties)
		if err == nil && slices.Contains(snap.Enrich(authz.Principal{Subject: u.Subject}).Roles, RoleAdmin) {
			return nil // at least one active administrator remains
		}
	}
	return fmt.Errorf("this change would leave no active administrator: %w", graph.ErrInvalid)
}
