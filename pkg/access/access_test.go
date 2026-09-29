package access_test

import (
	"context"
	"slices"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

func setup(t *testing.T) (*graph.Graph, *access.Authorizer) {
	t.Helper()
	g := graph.New(graph.NewMemory())
	a, err := access.NewAuthorizer(&access.Directory{Graph: g, TTL: 1})
	if err != nil {
		t.Fatal(err)
	}
	return g, a
}

func can(t *testing.T, a authz.Authorizer, p authz.Principal, res, act string) bool {
	t.Helper()
	ok, err := a.Authorize(context.Background(), authz.Request{Subject: p, Action: act, Resource: authz.Resource{Type: res, Org: p.Org}})
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDefaultsWhileTheGraphHasNoPolicy(t *testing.T) {
	_, a := setup(t)
	admin := authz.Principal{Subject: "root", Roles: []string{"admin"}}
	contrib := authz.Principal{Subject: "c", Org: "o", Roles: []string{"contributor"}}
	if !can(t, a, admin, "policy", "write") {
		t.Fatal("admin denied on an empty graph")
	}
	if !can(t, a, contrib, "process", "start") || can(t, a, contrib, "methodology", "write") {
		t.Fatal("default policies not applied on an empty graph")
	}
	if can(t, a, authz.Principal{}, "process", "start") {
		t.Fatal("anonymous allowed")
	}
}

func TestPoliciesComeFromNodes(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if seeded, err := graphsvc.SeedAccess(ctx, g); err != nil || !seeded {
		t.Fatalf("seed: %v %v", seeded, err)
	}
	if seeded, err := graphsvc.SeedAccess(ctx, g); err != nil || seeded {
		t.Fatalf("seed must be idempotent: %v %v", seeded, err)
	}
	contrib := authz.Principal{Subject: "c", Org: "o", Roles: []string{"contributor"}}
	if !can(t, a, contrib, "process", "start") {
		t.Fatal("seeded default policy not applied")
	}
	// a new rule authorises what the defaults do not
	if can(t, a, contrib, "report", "read-all") {
		t.Fatal("unexpected access")
	}
	if err := graphsvc.SeedPolicy(ctx, g, authz.Policy{Rule: `hasRole(r.sub, "contributor")`, Resource: "report", Action: "read-all", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if !can(t, a, contrib, "report", "read-all") {
		t.Fatal("policy node not applied")
	}
}

func TestAdministratorsAreNeverLockedOut(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedPolicy(ctx, g, authz.Policy{Rule: `hasRole(r.sub, "admin")`, Resource: "*", Action: "*", Effect: "deny"}); err != nil {
		t.Fatal(err)
	}
	admin := authz.Principal{Subject: "root", Roles: []string{"admin"}}
	if !can(t, a, admin, "policy", "write") || !can(t, a.Floor(), admin, "policy", "write") {
		t.Fatal("a deny policy locked the administrator out")
	}
}

func TestUserNodeGrantsRolesAndUnit(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUser(ctx, g, access.User{Subject: "alice", Roles: []string{"admin"}, Unit: "team-a"}); err != nil {
		t.Fatal(err)
	}
	alice := authz.Principal{Subject: "alice"}
	if !can(t, a, alice, "policy", "write") || !can(t, a.Floor(), alice, "policy", "write") {
		t.Fatal("the roles of the User node were not granted")
	}
	dir := &access.Directory{Graph: g, TTL: 1}
	if p := dir.Enrich(ctx, alice); p.Org != "team-a" || len(p.Roles) != 1 {
		t.Fatalf("enriched principal: %+v", p)
	}
	if p := dir.Enrich(ctx, authz.Principal{Subject: "bob"}); len(p.Roles) != 0 || p.Org != "" {
		t.Fatalf("unknown subject must be left alone: %+v", p)
	}
}

func TestRolesHeldInAUnitHoldBelowIt(t *testing.T) {
	unit := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: mcp.NodeTypeOrgUnit, Namespace: "organisation"}
	}
	nodes := []domain.Node{unit("1", "ORG-DEFAULT"), unit("2", "DEP-IT"), unit("3", "TEAM-PAY")}
	partOf := func(from, to string) domain.Link {
		return domain.Link{Type: access.LinkPartOf, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	s := access.BuildSnapshot("b", nodes, []domain.Link{partOf("3", "2"), partOf("2", "1")})
	if got := s.Chain("TEAM-PAY"); !slices.Equal(got, []string{"TEAM-PAY", "DEP-IT", "ORG-DEFAULT"}) {
		t.Fatalf("chain %v", got)
	}
	if got := s.Chain("UNKNOWN"); !slices.Equal(got, []string{"UNKNOWN", "ORG-DEFAULT"}) {
		t.Fatalf("an unknown unit is under the default unit: %v", got)
	}
}
