package graphsvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
)

// Every RPC is identified: an anonymous request is refused before it reaches the graph, mutating or not, and the
// handler receives the principal of the request (Handler.Identify).
func TestAnonymousCallsAreRefused(t *testing.T) {
	g := typedGraph(t)
	h := &graphsvc.Handler{Graph: g}
	mux := http.NewServeMux()
	mux.Handle(graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.Identify())))
	mux.Handle(changev1connect.NewChangeServiceHandler(h, connect.WithInterceptors(h.Identify())))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := newRPCClient(srv.Client(), srv.URL)
	if _, err := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL).ListNamespaces(context.Background(), connect.NewRequest(&graphv1.ListNamespacesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ListNamespaces: %v", err)
	}
	ctx := context.Background()

	refused := func(err error) bool { return connect.CodeOf(err) == connect.CodeUnauthenticated }
	_, err := cl.CreateChange(ctx, connect.NewRequest(&graphv1.CreateChangeRequest{Title: "t", Intent: "i", Namespace: "organisation"}))
	if !refused(err) {
		t.Fatalf("CreateChange: %v", err)
	}
	_, err = cl.ImpactNodeCreate(ctx, connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: "C1", Key: "X", Type: "organisation@OrgUnit"}))
	if !refused(err) {
		t.Fatalf("ImpactNodeCreate: %v", err)
	}
	_, err = cl.UpdateChange(ctx, connect.NewRequest(&graphv1.UpdateChangeRequest{Id: "C1"}))
	if !refused(err) {
		t.Fatalf("UpdateChange: %v", err)
	}
	_, err = cl.ListChanges(ctx, connect.NewRequest(&graphv1.ListChangesRequest{}))
	if !refused(err) {
		t.Fatalf("ListChanges: %v", err)
	}

	// an identified request gets through
	req := connect.NewRequest(&graphv1.ListChangesRequest{})
	identity.SetHeaders(authz.Principal{Subject: "alice"}, req.Header())
	if _, err := cl.ListChanges(ctx, req); err != nil {
		t.Fatalf("identified ListChanges: %v", err)
	}
}

// A platform service (system:registry, ...) calling the graph never becomes a User, so it cannot take the
// first-administrator grant (ADR 0040): the first person who signs in after it does.
func TestServiceSubjectsDoNotTakeTheFirstAdminGrant(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.Identify(), h.EnsureCaller()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle(changev1connect.NewChangeServiceHandler(h, connect.WithInterceptors(h.Identify(), h.EnsureCaller())))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := newRPCClient(srv.Client(), srv.URL)
	as := func(subject string) {
		req := connect.NewRequest(&graphv1.ListChangesRequest{})
		identity.SetHeaders(authz.Principal{Subject: subject}, req.Header())
		if _, err := cl.ListChanges(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	as("system:registry")
	as("system:trigger:nightly")
	if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.UserKey("system:registry")); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("a service must not become a User: %v", err)
	}
	as("alice")
	n, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.PlatformAssignmentKey(access.UserKey("alice")))
	if err != nil {
		t.Fatalf("alice's platform assignment: %v", err)
	}
	asg, err := access.AssignmentFromProps(n.Properties)
	if err != nil || !slices.Contains(asg.Roles, access.RoleAdmin) {
		t.Fatalf("alice must be the first administrator: %+v %v", asg, err)
	}
}
