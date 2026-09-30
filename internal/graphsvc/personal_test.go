package graphsvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// A personal change (ADR 0037) belongs to its owner: another caller neither sees nor touches it, and only its
// owner removes it, as long as it landed nothing.
func TestPersonalChange(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.PersonalScope()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	as := func(subject string) func(r connect.AnyRequest) {
		return func(r connect.AnyRequest) { identity.SetHeaders(authz.Principal{Subject: subject}, r.Header()) }
	}
	call := func(subject string, r connect.AnyRequest) { as(subject)(r) }

	base, err := g.CreateBaseline(ctx, "platform", "B0", nil)
	if err != nil {
		t.Fatal(err)
	}
	baseline := connect.NewRequest(&graphv1.ListChangesRequest{})
	call("bob", baseline)
	before, err := cl.ListChanges(ctx, baseline)
	if err != nil {
		t.Fatal(err)
	}
	seeded := len(before.Msg.Changes) // the changes SeedDefaults made, visible to everyone (not personal)

	req := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "prefs", Namespace: "platform", BaselineId: string(base.ID), OwnBranch: true, OwnerOrg: graphsvc.OwnerMe})
	call("alice", req)
	created, err := cl.CreateChange(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Change.Id
	if got := created.Msg.Change.OwnerOrg; got != "USR:alice" {
		t.Fatalf("owner = %q", got)
	}

	// bob: not found, absent from the lists, cannot remove it, cannot claim alice's unit
	get := connect.NewRequest(&graphv1.GetChangeRequest{Id: id})
	call("bob", get)
	if _, err := cl.GetChange(ctx, get); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob GetChange: %v", err)
	}
	del := connect.NewRequest(&graphv1.DeleteChangeRequest{ChangeId: id})
	call("bob", del)
	if _, err := cl.DeleteChange(ctx, del); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("bob DeleteChange: %v", err)
	}
	list := connect.NewRequest(&graphv1.ListChangesRequest{})
	call("bob", list)
	if l, err := cl.ListChanges(ctx, list); err != nil || len(l.Msg.Changes) != seeded {
		t.Fatalf("bob ListChanges = %v, %v", l, err)
	}
	claim := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "x", Namespace: "platform", BaselineId: string(base.ID), OwnerOrg: "USR:alice"})
	call("bob", claim)
	if _, err := cl.CreateChange(ctx, claim); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("bob claims alice's unit: %v", err)
	}

	// alice sees it, and no sub-change is split off it
	list = connect.NewRequest(&graphv1.ListChangesRequest{})
	call("alice", list)
	if l, err := cl.ListChanges(ctx, list); err != nil || len(l.Msg.Changes) != seeded+1 {
		t.Fatalf("alice ListChanges = %v, %v", l, err)
	}
	sub := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "sub", ParentId: id, OwnerOrg: "@me"})
	call("alice", sub)
	if _, err := cl.CreateChange(ctx, sub); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("sub-change of a personal change: %v", err)
	}

	// discarding removes it for good
	del = connect.NewRequest(&graphv1.DeleteChangeRequest{ChangeId: id})
	call("alice", del)
	if _, err := cl.DeleteChange(ctx, del); err != nil {
		t.Fatalf("alice DeleteChange: %v", err)
	}
	get = connect.NewRequest(&graphv1.GetChangeRequest{Id: id})
	call("alice", get)
	if _, err := cl.GetChange(ctx, get); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetChange after the purge: %v", err)
	}
}

// A User node is created automatically the first time a subject is seen (ADR 0039), not only when they
// resolve "@me": any authenticated call ensures it, via the EnsureCaller interceptor. It doubles as the
// subject's personal unit (organisation@User extends organisation@OrgUnit, same "USR:<subject>" key).
func TestUserCreatedAutomatically(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.PersonalScope(), h.EnsureCaller()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)

	// bob only lists changes (no @me, no personal change involved): he still gets a User node.
	list := connect.NewRequest(&graphv1.ListChangesRequest{})
	identity.SetHeaders(authz.Principal{Subject: "bob"}, list.Header())
	if _, err := cl.ListChanges(ctx, list); err != nil {
		t.Fatal(err)
	}
	n, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "USR:bob")
	if err != nil {
		t.Fatalf("bob's User node: %v", err)
	}
	if n.Type != access.NodeTypeUser {
		t.Fatalf("type = %s, want %s", n.Type, access.NodeTypeUser)
	}
	if n.Properties["subject"] != "bob" {
		t.Fatalf("properties = %+v", n.Properties)
	}
	// calling again must not create a second version (deduplicated per subject)
	list2 := connect.NewRequest(&graphv1.ListChangesRequest{})
	identity.SetHeaders(authz.Principal{Subject: "bob"}, list2.Header())
	if _, err := cl.ListChanges(ctx, list2); err != nil {
		t.Fatal(err)
	}
	again, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "USR:bob")
	if err != nil || again.Version != n.Version {
		t.Fatalf("a second call must not write again: %+v, %v", again, err)
	}
}

// EnsureUser must not create a broken User (no member_of, no shot at the first-admin bootstrap) when the
// default organisation does not exist yet — SeedDefaults can still be seeding at startup (ADR 0040). Failing
// leaves nothing behind, so a later, correctly-timed call for the same subject still succeeds.
func TestEnsureUserWaitsForDefaultOrg(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())

	if err := graphsvc.EnsureUser(ctx, g, "alice"); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("EnsureUser before SeedDefaults = %v, want ErrNotFound", err)
	}
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "USR:alice"); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("a failed EnsureUser must leave no node: %v", err)
	}

	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.EnsureUser(ctx, g, "alice"); err != nil {
		t.Fatalf("EnsureUser after SeedDefaults: %v", err)
	}
	n, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "USR:alice")
	if err != nil {
		t.Fatalf("alice's User node: %v", err)
	}
	u, err := access.UserFromProps(n.Properties)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(u.Roles, "admin") {
		t.Fatalf("first user must be admin: %+v", u)
	}
	links, err := g.OutLinksOf(ctx, n.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var linked bool
	for _, l := range links {
		if l.Type == access.LinkMemberOf {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("alice must be member_of the default org: %+v", links)
	}

	// a second subject is not the first user: no admin role
	if err := graphsvc.EnsureUser(ctx, g, "bob"); err != nil {
		t.Fatal(err)
	}
	bobNode, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, "USR:bob")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := access.UserFromProps(bobNode.Properties)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(bob.Roles, "admin") {
		t.Fatalf("second user must not be admin: %+v", bob)
	}
}
