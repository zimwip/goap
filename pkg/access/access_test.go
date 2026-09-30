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

// An Assignment grants a role on a project (ADR 0039): a step check with no token-level role passes when
// an Assignment gives the principal's org the RACI role for the change's project, and fails for an
// unrelated project. Reuses the default step policy (hasRoleIn(r.sub, r.obj.Role, r.obj)): the merged
// project role rides the same unscoped-role path as a token role would.
func TestAssignmentGrantsRoleOnAProject(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	team, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, mcp.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	teamRef := team.Ref()
	edits := []graph.NodeEdit{
		{Key: "PROJ-X", Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: "X"}.Props(), Rationale: "t"},
		{Key: "PROJ-Y", Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: "Y"}.Props(), Rationale: "t"},
		{Key: "ASG:team-a/PROJ-X", Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{"developer"}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &teamRef}, {Type: access.LinkAssignsProject, ToKey: "PROJ-X"}}},
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: mcp.NamespaceOrganisation, Title: "assignment", Baseline: head.ID, By: "test", BaselineName: "assignment", Edits: edits}); err != nil {
		t.Fatal(err)
	}

	dev := authz.Principal{Subject: "dev", Org: "team-a"}
	step := func(project string) authz.Resource {
		return authz.Resource{Type: "step", Org: "team-a", ProjectID: project, Role: "developer"}
	}
	ok, err := a.Authorize(ctx, authz.Request{Subject: dev, Action: "perform", Resource: step("PROJ-X")})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the assignment must grant developer on PROJ-X")
	}
	ok, err = a.Authorize(ctx, authz.Request{Subject: dev, Action: "perform", Resource: step("PROJ-Y")})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("the assignment must not grant developer on an unrelated project")
	}
}

func TestProjectChainAndRoles(t *testing.T) {
	proj := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeProjectUnit, Namespace: "organisation"}
	}
	unit := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: mcp.NodeTypeOrgUnit, Namespace: "organisation"}
	}
	asg := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeAssignment, Namespace: "organisation",
			Properties: access.Assignment{Roles: []string{"developer"}}.Props()}
	}
	nodes := []domain.Node{
		proj("1", "PROJ-ROOT"), proj("2", "PROJ-A"), proj("3", "PROJ-A1"),
		unit("4", "DEP-IT"), asg("5", "ASG:DEP-IT/PROJ-A"),
	}
	lnk := func(typ, from, to string) domain.Link {
		return domain.Link{Type: typ, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	links := []domain.Link{
		lnk(access.LinkProjectPartOf, "1", "1"), // root self-link
		lnk(access.LinkProjectPartOf, "2", "1"),
		lnk(access.LinkProjectPartOf, "3", "2"),
		lnk(access.LinkAssignsOrg, "5", "4"),
		lnk(access.LinkAssignsProject, "5", "2"),
	}
	s := access.BuildSnapshot("b", nodes, links)
	if got := s.ProjectChain("PROJ-A1"); !slices.Equal(got, []string{"PROJ-A1", "PROJ-A", "PROJ-ROOT"}) {
		t.Fatalf("project chain %v", got)
	}
	if got := s.ProjectChain("PROJ-ROOT"); !slices.Equal(got, []string{"PROJ-ROOT"}) {
		t.Fatalf("root project chain must not loop on its own self-link: %v", got)
	}
	// the assignment is on PROJ-A: it holds for PROJ-A and PROJ-A1 (below it), not for PROJ-ROOT alone
	if got := s.ProjectRoles(s.Chain("DEP-IT"), s.ProjectChain("PROJ-A1")); !slices.Equal(got, []string{"developer"}) {
		t.Fatalf("roles on a descendant project: %v", got)
	}
	if got := s.ProjectRoles(s.Chain("DEP-IT"), s.ProjectChain("PROJ-ROOT")); len(got) != 0 {
		t.Fatalf("the assignment must not leak to the root project: %v", got)
	}
	if got := s.ProjectRoles(s.Chain("OTHER-UNIT"), s.ProjectChain("PROJ-A1")); len(got) != 0 {
		t.Fatalf("the assignment must not leak to another unit: %v", got)
	}
}
