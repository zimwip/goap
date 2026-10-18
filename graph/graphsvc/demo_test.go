package graphsvc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/identity"
)

// The demo data is loaded on demand by an administrator; a deployment shipping none answers unimplemented.
func TestLoadDemoDataIsForAdministrators(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	var by authz.Principal
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g, Floor: floor, Demo: func(ctx context.Context, _ *graph.Graph) (bool, string, error) {
		by = authz.From(ctx)
		return true, "PROJ-DEMO", nil
	}}
	load := func(roles string) (*connect.Response[graphv1.LoadDemoDataResponse], error) {
		req := connect.NewRequest(&graphv1.LoadDemoDataRequest{})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		return h.LoadDemoData(ctx, req)
	}
	if _, err := load("methodologist"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("only administrators load the demo: %v", err)
	}
	r, err := load("admin")
	if err != nil || !r.Msg.Loaded || r.Msg.Project != "PROJ-DEMO" {
		t.Fatalf("admin load = %v, %v", r, err)
	}
	if !by.System() || by.EndUser() != "u" {
		t.Errorf("the seed acts as the graph service for the administrator: %+v", by)
	}
	h.Demo = nil
	if _, err := load("admin"); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("no demo: %v", err)
	}
}
