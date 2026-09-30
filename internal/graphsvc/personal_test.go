package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if l, err := cl.ListChanges(ctx, list); err != nil || len(l.Msg.Changes) != 0 {
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
	if l, err := cl.ListChanges(ctx, list); err != nil || len(l.Msg.Changes) != 1 {
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
