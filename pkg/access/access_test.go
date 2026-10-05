package access_test

import (
	"context"
	"slices"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/typecat"
)

// setup returns a graph judged by the domains of the repository (ADR 0012), so a User node seeded here goes
// through the same user lifecycle (ADR 0048) a real deployment enforces.
func setup(t *testing.T) (*graph.Graph, *access.Authorizer) {
	t.Helper()
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
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

func TestAdminAssignmentAndUnitOfTheUserNode(t *testing.T) {
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
	if err := graphsvc.SeedUser(ctx, g, access.User{Subject: "alice", Unit: "team-a"}); err != nil {
		t.Fatal(err)
	}
	alice0, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.UserKey("alice"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	aliceRef := alice0.Ref()
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "alice administers", Baseline: head.ID, By: "test", Edits: []graph.NodeEdit{
		{Key: access.PlatformAssignmentKey(access.UserKey("alice")), Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{access.RoleAdmin}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &aliceRef}}},
	}}); err != nil {
		t.Fatal(err)
	}
	alice := authz.Principal{Subject: "alice"}
	if !can(t, a, alice, "policy", "write") || !can(t, a.Floor(), alice, "policy", "write") {
		t.Fatal("the platform admin assignment of the User node was not granted")
	}
	dir := &access.Directory{Graph: g, TTL: 1}
	if p := dir.Enrich(ctx, alice); p.Org != "team-a" || !slices.Equal(p.Roles, []string{access.RoleAdmin}) {
		t.Fatalf("enriched principal: %+v", p)
	}
	if p := dir.Enrich(ctx, authz.Principal{Subject: "bob"}); len(p.Roles) != 0 || p.Org != "" {
		t.Fatalf("unknown subject must be left alone: %+v", p)
	}
}

func TestRolesHeldInAUnitHoldBelowIt(t *testing.T) {
	unit := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeOrgUnit, Namespace: "organisation"}
	}
	nodes := []domain.Node{unit("1", "ORG-DEFAULT"), unit("2", "DEP-IT"), unit("3", "TEAM-PAY")}
	partOf := func(from, to string) domain.Link {
		return domain.Link{Type: access.LinkPartOf, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, []domain.Link{partOf("3", "2"), partOf("2", "1")})
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
	team, err := g.NodeByKey(ctx, access.NamespaceOrganisation, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	root, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.DefaultProject)
	if err != nil {
		t.Fatal(err)
	}
	rootRef := root.Ref()
	teamRef := team.Ref()
	edits := []graph.NodeEdit{
		{Key: "PROJ-X", Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: "X"}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkProjectPartOf, To: &rootRef}}},
		{Key: "PROJ-Y", Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: "Y"}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkProjectPartOf, To: &rootRef}}},
		{Key: "ASG:team-a/PROJ-X", Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{"developer"}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &teamRef}, {Type: access.LinkAssignsProject, ToKey: "PROJ-X"}}},
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "assignment", Baseline: head.ID, By: "test", BaselineName: "assignment", Edits: edits}); err != nil {
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

// A platform-wide Assignment (no assigns_project link, ADR 0046) grants a role everywhere, independent of
// any project's methodologies: a project needing no methodology can still be reached by its members once an
// administrator grants them "reader" platform-wide.
func TestPlatformAssignmentGrantsRoleEverywhere(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "other-org", "Other", "org", ""); err != nil {
		t.Fatal(err)
	}
	team, err := g.NodeByKey(ctx, access.NamespaceOrganisation, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	teamRef := team.Ref()
	edits := []graph.NodeEdit{
		{Key: access.PlatformAssignmentKey("team-a"), Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{access.RoleReader}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &teamRef}}},
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "platform assignment", Baseline: head.ID, By: "test", BaselineName: "platform assignment", Edits: edits}); err != nil {
		t.Fatal(err)
	}

	reader := authz.Principal{Subject: "dev", Org: "team-a"}
	// a platform reader reads an object of an unrelated org, past the usual org/project scoping
	if ok, err := a.Authorize(ctx, authz.Request{Subject: reader, Action: "read", Resource: authz.Resource{Type: "x", Org: "other-org"}}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("the platform assignment must grant read everywhere")
	}
	stranger := authz.Principal{Subject: "stranger", Org: "other-org"}
	if ok, err := a.Authorize(ctx, authz.Request{Subject: stranger, Action: "read", Resource: authz.Resource{Type: "x", Org: "team-a"}}); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("a unit without the platform assignment must not read another org's resource")
	}
}

// Administration is itself a platform role now (ADR 0047): a unit granted "admin" through a platform
// Assignment reaches the floor (Authorizer.Floor, which calls Enrich alone, never the fuller Authorize) the
// same way a user's own Assignment does — a deny policy can never lock it out (ADR 0020, 0043).
func TestPlatformAssignmentGrantsAdminPastTheFloor(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedPolicy(ctx, g, authz.Policy{Rule: `hasRole(r.sub, "admin")`, Resource: "*", Action: "*", Effect: "deny"}); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	team, err := g.NodeByKey(ctx, access.NamespaceOrganisation, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	teamRef := team.Ref()
	edits := []graph.NodeEdit{
		{Key: access.PlatformAssignmentKey("team-a"), Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{access.RoleAdmin}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &teamRef}}},
	}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "admin assignment", Baseline: head.ID, By: "test", BaselineName: "admin assignment", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	dev := authz.Principal{Subject: "dev", Org: "team-a"}
	if !can(t, a, dev, "policy", "write") || !can(t, a.Floor(), dev, "policy", "write") {
		t.Fatal("the platform admin assignment must pass the floor, deny policy notwithstanding")
	}
}

func TestProjectChainAndRoles(t *testing.T) {
	proj := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeProjectUnit, Namespace: "organisation"}
	}
	unit := func(id, key string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: access.NodeTypeOrgUnit, Namespace: "organisation"}
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
	s := access.BuildSnapshot(typecat.Builtin().Structures(), "b", nodes, links)
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

// A user's roles depend on the project (ADR 0043): an Assignment to the user itself (User extends OrgUnit)
// and one to its unit add up on the project they name, and nowhere else. A role the user's unit holds does not
// pass to someone outside it acting on that unit's change.
func TestRolesDependOnTheProject(t *testing.T) {
	ctx := context.Background()
	g, a := setup(t)
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedUser(ctx, g, access.User{Subject: "eve", Unit: "team-a"}); err != nil {
		t.Fatal(err)
	}
	ref := func(key string) *domain.NodeRef {
		n, err := g.NodeByKey(ctx, access.NamespaceOrganisation, key)
		if err != nil {
			t.Fatal(err)
		}
		r := n.Ref()
		return &r
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	root := ref(access.DefaultProject)
	project := func(key string) graph.NodeEdit {
		return graph.NodeEdit{Key: key, Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: key}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkProjectPartOf, To: root}}}
	}
	assign := func(org, proj string, roles ...string) graph.NodeEdit {
		return graph.NodeEdit{Key: "ASG:" + org + "/" + proj, Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: roles}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: ref(org)}, {Type: access.LinkAssignsProject, ToKey: proj}}}
	}
	edits := []graph.NodeEdit{project("PROJ-A"), project("PROJ-B"), project("PROJ-C"),
		assign("USR:eve", "PROJ-A", "developer"), assign("team-a", "PROJ-A", "tester"), assign("USR:eve", "PROJ-B", "tech_lead")}
	if _, err := g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "assignments", Baseline: head.ID, By: "test", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	eve := authz.Principal{Subject: "eve"}
	run := func(who authz.Principal, project string, roles ...string) bool {
		ok, err := a.Authorize(ctx, authz.Request{Subject: who, Action: "run", Resource: authz.Resource{Type: "action", ProjectID: project, Roles: roles}})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, tc := range []struct {
		project string
		roles   []string
		want    bool
	}{
		{"PROJ-A", []string{"developer"}, true},  // held by eve herself
		{"PROJ-A", []string{"tester"}, true},     // held by her unit
		{"PROJ-A", []string{"tech_lead"}, false}, // held on another project
		{"PROJ-B", []string{"tech_lead"}, true},
		{"PROJ-B", []string{"developer"}, false},
		{"PROJ-C", nil, false}, // not a member of PROJ-C at all
		{"PROJ-B", nil, true},  // any role on PROJ-B
	} {
		if got := run(eve, tc.project, tc.roles...); got != tc.want {
			t.Errorf("eve on %s for %v: %v, want %v", tc.project, tc.roles, got, tc.want)
		}
	}
	// someone outside team-a acting on a change team-a holds does not get team-a's roles
	mallory := authz.Principal{Subject: "mallory", Org: "other"}
	ok, err := a.Authorize(ctx, authz.Request{Subject: mallory, Action: "run", Resource: authz.Resource{Type: "action", Org: "team-a", ProjectID: "PROJ-A", Roles: []string{"tester"}}})
	if err != nil || ok {
		t.Fatalf("mallory got team-a's role: %v %v", ok, err)
	}
}

// The snapshot reads the organisation and the projects the graph names (ADR 0054), never types of its own: with
// structures tagged by another domain, its units, parent links and roots are theirs.
func TestSnapshotFollowsTheStructures(t *testing.T) {
	st := domain.Structures{
		{Structure: domain.Structure{Kind: domain.StructureOrganisation, Type: "hr@Team", Namespace: "hr", Parent: "hr@within", Root: "HR-ROOT"}, Types: []string{"hr@Team"}},
		{Structure: domain.Structure{Kind: domain.StructureProject, Type: "hr@Programme", Namespace: "hr", Parent: "hr@inside", Root: "PRG-ROOT", SelfParent: true}, Types: []string{"hr@Programme"}},
	}
	n := func(id, key, typ string) domain.Node {
		return domain.Node{ID: domain.NodeID(id), Key: key, Type: typ, Namespace: "hr"}
	}
	l := func(typ, from, to string) domain.Link {
		return domain.Link{Type: typ, From: domain.NodeRef{ID: domain.NodeID(from)}, To: domain.NodeRef{ID: domain.NodeID(to)}}
	}
	nodes := []domain.Node{n("1", "HR-ROOT", "hr@Team"), n("2", "PAY", "hr@Team"), n("3", "PRG-ROOT", "hr@Programme"), n("4", "PRG-A", "hr@Programme"),
		n("5", "OLD", "organisation@OrgUnit")}
	links := []domain.Link{l("hr@within", "2", "1"), l("hr@inside", "4", "3"), l("hr@inside", "3", "3"), l(access.LinkPartOf, "5", "1")}
	s := access.BuildSnapshot(st, "b", nodes, links)
	if got := s.Chain("PAY"); !slices.Equal(got, []string{"PAY", "HR-ROOT"}) {
		t.Fatalf("chain %v", got)
	}
	if got := s.Chain("OLD"); !slices.Equal(got, []string{"OLD", "HR-ROOT"}) {
		t.Fatalf("a link the structures do not name is no hierarchy: %v", got)
	}
	if got := s.ProjectChain("PRG-A"); !slices.Equal(got, []string{"PRG-A", "PRG-ROOT"}) {
		t.Fatalf("project chain %v", got)
	}
	if got := s.SubjectChain(authz.Principal{Subject: "x"}); !slices.Equal(got, []string{access.UserKey("x"), "HR-ROOT"}) {
		t.Fatalf("a subject of no unit is under the root unit: %v", got)
	}
}
