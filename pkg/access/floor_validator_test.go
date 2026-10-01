package access_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// floorGraph returns a graph judged by the domains of the repository, with the admin-floor validator wired
// the way cmd/goap-dev and cmd/graph wire it (ADR 0048).
func floorGraph(t *testing.T) *graph.Graph {
	t.Helper()
	ds, err := methodology.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}}
	return g
}

// TestAdminFloorBootstrapNotRejected is implicit in every call to EnsureUser below (the first user's own
// bootstrap commit creates the User and its admin Assignment together; if the validator rejected its own
// bootstrap, EnsureUser would fail here).
func TestAdminFloorRejectsRemovingLastAdmin(t *testing.T) {
	ctx := context.Background()
	g := floorGraph(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.EnsureUser(ctx, g, "alice"); err != nil {
		t.Fatal(err)
	}
	alice, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.UserKey("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if alice.State != "active" {
		t.Fatalf("alice should be active, got %q", alice.State)
	}

	head, err := g.BranchHead(ctx, mcp.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	ref := alice.Ref()
	_, err = g.Commit(ctx, graph.Commit{Namespace: mcp.NamespaceOrganisation, Title: "deactivate alice", Baseline: head.ID,
		Edits: []graph.NodeEdit{{Pre: &ref, State: "deactivated", Rationale: "test"}}})
	if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "no active administrator") {
		t.Fatalf("expected admin-floor rejection, got %v", err)
	}
	if n, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.UserKey("alice")); err != nil || n.State != "active" {
		t.Fatalf("alice must stay active: %+v %v", n, err)
	}
}

// Deactivating the only administrator while granting admin to another active user, in the same commit,
// succeeds: the floor check looks at the change's resulting state, not each impacted node in isolation.
func TestAdminFloorAllowsHandoffInOneCommit(t *testing.T) {
	ctx := context.Background()
	g := floorGraph(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.EnsureUser(ctx, g, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.EnsureUser(ctx, g, "bob"); err != nil {
		t.Fatal(err)
	}
	alice, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.UserKey("alice"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.UserKey("bob"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, mcp.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	aliceRef, bobRef := alice.Ref(), bob.Ref()
	asg := access.Assignment{Roles: []string{access.RoleAdmin}, Description: "bob becomes administrator"}
	_, err = g.Commit(ctx, graph.Commit{Namespace: mcp.NamespaceOrganisation, Title: "admin handoff", Baseline: head.ID, Edits: []graph.NodeEdit{
		{Key: access.PlatformAssignmentKey(access.UserKey("bob")), Type: access.NodeTypeAssignment, Props: asg.Props(),
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &bobRef}}, Rationale: "test"},
		{Pre: &aliceRef, State: "deactivated", Rationale: "test"},
	}})
	if err != nil {
		t.Fatalf("handoff should succeed: %v", err)
	}
	if n, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.UserKey("alice")); err != nil || n.State != "deactivated" {
		t.Fatalf("alice should be deactivated: %+v %v", n, err)
	}
}
