package graphsvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

type moveEnv struct {
	g    *graph.Graph
	a    *access.Authorizer
	base domain.Baseline
}

// moveWorld seeds the projects X, Y (both applying m1), Z (m2) and W (m1), a team whose developers work on X, Y and Z
// but not on W.
func moveWorld(t *testing.T) moveEnv {
	t.Helper()
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	dir := &access.Directory{Graph: g, TTL: 1}
	a, err := access.NewAuthorizer(dir)
	if err != nil {
		t.Fatal(err)
	}
	g.ProjectMoveGate = graphsvc.ProjectMoveGate(a, dir)
	// the methodology of a change applying to both projects is its guardian's (ADR 0098)
	g.Guardians = map[string]graph.Guardian{registrysvc.GuardianName: registrysvc.Guardian{Service: &registrysvc.Service{Store: registrysvc.NewMemoryStore()}, Directory: dir}}
	g.DefaultGuardian = registrysvc.GuardianName
	if err := graphsvc.SeedUnit(ctx, g, "team-a", "Team A", "team", ""); err != nil {
		t.Fatal(err)
	}
	root := mustNode(t, g, access.DefaultProject).Ref()
	teamRef := mustNode(t, g, "team-a").Ref()
	edits := []graph.NodeEdit{}
	for key, ms := range map[string][]string{"PROJ-X": {"m1"}, "PROJ-Y": {"m1"}, "PROJ-Z": {"m2"}, "PROJ-W": {"m1"}} {
		edits = append(edits, graph.NodeEdit{Key: key, Type: access.NodeTypeProjectUnit, Props: access.ProjectUnit{Name: key, Methodologies: ms}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkProjectPartOf, To: &root}}})
	}
	for _, p := range []string{"PROJ-X", "PROJ-Y", "PROJ-Z"} {
		edits = append(edits, graph.NodeEdit{Key: "ASG:team-a/" + p, Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{"developer"}}.Props(), Rationale: "t",
			Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, To: &teamRef}, {Type: access.LinkAssignsProject, ToKey: p}}})
	}
	head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(ctx, graph.Commit{ProjectID: access.DefaultProject, Namespace: access.NamespaceOrganisation, Title: "projects", Baseline: head.ID, By: "test", BaselineName: "projects", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	return moveEnv{g: g, a: a}
}

func mustNode(t *testing.T, g *graph.Graph, key string) domain.Node {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), access.NamespaceOrganisation, key)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e moveEnv) change(t *testing.T, project, methodology string) domain.Change {
	t.Helper()
	ctx := authz.With(context.Background(), graphsvc.SystemPrincipal)
	c, err := e.g.CreateChange(ctx, graph.NewChange{Title: "c", Methodology: methodology, ProjectID: project, Namespace: "platform", OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type authorizerFunc func(context.Context, authz.Request) (bool, error)

func (f authorizerFunc) Authorize(ctx context.Context, r authz.Request) (bool, error) {
	return f(ctx, r)
}

// A change moves between projects the caller works on (ProjectMoveGate) and whose methodologies both include the
// change's (its guardian, registrysvc.Guardian, ADR 0091, 0098).
func TestProjectMoveGate(t *testing.T) {
	e := moveWorld(t)
	dev := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "team-a"})
	admin := authz.With(context.Background(), authz.Principal{Subject: "root", Roles: []string{access.RoleAdmin}})

	c := e.change(t, "PROJ-X", "m1")
	sub, err := e.g.CreateChange(dev, graph.NewChange{Title: "s", ParentID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.g.MoveChange(dev, c.ID, "PROJ-Y"); err != nil || got.ProjectID != "PROJ-Y" {
		t.Fatalf("X to Y: %+v, %v", got, err)
	}
	if got, _ := e.g.Change(dev, sub.ID); got.ProjectID != "PROJ-Y" {
		t.Fatalf("sub-change at %s", got.ProjectID)
	}

	// the methodology is not listed by the target
	_, err = e.g.MoveChange(dev, c.ID, "PROJ-Z")
	if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "m1") || !strings.Contains(err.Error(), "PROJ-Z") {
		t.Fatalf("Y to Z: %v", err)
	}
	// ... or by the source
	z := e.change(t, "PROJ-Z", "m1")
	_, err = e.g.MoveChange(dev, z.ID, "PROJ-X")
	if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "PROJ-Z") {
		t.Fatalf("Z to X: %v", err)
	}
	// no access to the target
	if _, err := e.g.MoveChange(dev, c.ID, "PROJ-W"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Y to W without access: %v", err)
	}
	// ... which an administrator has
	if got, err := e.g.MoveChange(admin, c.ID, "PROJ-W"); err != nil || got.ProjectID != "PROJ-W" {
		t.Fatalf("admin Y to W: %+v, %v", got, err)
	}
	// a change with no methodology moves wherever the caller works
	free := e.change(t, "PROJ-X", "")
	if got, err := e.g.MoveChange(dev, free.ID, "PROJ-Z"); err != nil || got.ProjectID != "PROJ-Z" {
		t.Fatalf("no methodology: %+v, %v", got, err)
	}
	// a sub-change of another methodology holds the family back
	p := e.change(t, "PROJ-X", "m1")
	if _, err := e.g.CreateChange(dev, graph.NewChange{Title: "s", ParentID: p.ID, Methodology: "m2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.MoveChange(dev, p.ID, "PROJ-Y"); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("a sub-change of m2: %v", err)
	}
	if got, _ := e.g.Change(dev, p.ID); got.ProjectID != "PROJ-X" {
		t.Fatalf("a refused family stays: %s", got.ProjectID)
	}
}

// change:move is asked on the project the change leaves and the one it reaches.
func TestProjectMoveGateAsksChangeMoveOnBothProjects(t *testing.T) {
	e := moveWorld(t)
	var asked []string
	deny := authorizerFunc(func(_ context.Context, r authz.Request) (bool, error) {
		if r.Resource.Type == "change" && r.Action == "move" {
			asked = append(asked, r.Resource.ProjectID)
			return r.Resource.ProjectID != "PROJ-Y", nil
		}
		return true, nil
	})
	e.g.ProjectMoveGate = graphsvc.ProjectMoveGate(deny, &access.Directory{Graph: e.g, TTL: 1})
	dev := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "team-a"})
	c := e.change(t, "PROJ-X", "m1")
	if _, err := e.g.MoveChange(dev, c.ID, "PROJ-Y"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("change:move denied on the target: %v", err)
	}
	if len(asked) != 2 || asked[0] != "PROJ-X" || asked[1] != "PROJ-Y" {
		t.Fatalf("asked on %v", asked)
	}
}

// Over RPC (ADR 0091): a change is created in the project the request names, else the caller's active project, else
// the root project; MoveChange moves it, the caller's rights and the methodology rule applying as in the graph.
func TestMoveChangeRPC(t *testing.T) {
	ctx := context.Background()
	e := moveWorld(t)
	h := &graphsvc.Handler{Graph: e.g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.PersonalScope()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle(changev1connect.NewChangeServiceHandler(h, connect.WithInterceptors(h.PersonalScope())))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := newRPCClient(srv.Client(), srv.URL)
	as := func(p authz.Principal, r connect.AnyRequest) { identity.SetHeaders(p, r.Header()) }
	dev := authz.Principal{Subject: "dev", Org: "team-a", Project: "PROJ-X"}
	create := func(p authz.Principal, project string) *graphv1.Change {
		t.Helper()
		r := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "c", Namespace: "platform", OwnBranch: true, Methodology: "m1", ProjectId: project})
		as(p, r)
		out, err := cl.CreateChange(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out.Msg.Change
	}
	if c := create(dev, ""); c.ProjectId != "PROJ-X" {
		t.Fatalf("the active project of the caller: %s", c.ProjectId)
	}
	if c := create(authz.Principal{Subject: "dev", Org: "team-a"}, ""); c.ProjectId != access.DefaultProject {
		t.Fatalf("an empty claim is the root project: %s", c.ProjectId)
	}
	c := create(dev, "PROJ-Y")
	if c.ProjectId != "PROJ-Y" {
		t.Fatalf("the project of the request: %s", c.ProjectId)
	}
	mv := connect.NewRequest(&graphv1.MoveChangeRequest{ChangeId: c.Id, ProjectId: "PROJ-X"})
	as(dev, mv)
	moved, err := cl.MoveChange(ctx, mv)
	if err != nil || moved.Msg.Change.ProjectId != "PROJ-X" {
		t.Fatalf("move: %v %+v", err, moved)
	}
	bad := connect.NewRequest(&graphv1.MoveChangeRequest{ChangeId: c.Id, ProjectId: "PROJ-Z"})
	as(dev, bad)
	if _, err := cl.MoveChange(ctx, bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("methodology not applicable: %v", err)
	}
	none := connect.NewRequest(&graphv1.MoveChangeRequest{ChangeId: c.Id, ProjectId: "PROJ-W"})
	as(dev, none)
	if _, err := cl.MoveChange(ctx, none); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no access: %v", err)
	}
	// a personal change of someone else is not found
	personal := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "p", Namespace: "platform", OwnBranch: true, OwnerOrg: graphsvc.OwnerMe, ProjectId: "PROJ-X"})
	as(authz.Principal{Subject: "alice"}, personal)
	pc, err := cl.CreateChange(ctx, personal)
	if err != nil {
		t.Fatal(err)
	}
	other := connect.NewRequest(&graphv1.MoveChangeRequest{ChangeId: pc.Msg.Change.Id, ProjectId: "PROJ-Y"})
	as(dev, other)
	if _, err := cl.MoveChange(ctx, other); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a personal change of another: %v", err)
	}
}
