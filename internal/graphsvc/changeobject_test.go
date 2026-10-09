package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// Change objects through the service (ADR 0098): the client writes, reads and submits them; the labels of the log are
// carried too.
func TestChangeObjectsThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle(changev1connect.NewChangeServiceHandler(&graphsvc.Handler{Graph: g}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphsvc.NewClient(srv.Client(), srv.URL)

	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	// the built-in execution types are known to an untyped graph
	out, err := cl.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "execution@Methodology", Value: map[string]any{"name": "sdlc", "version": "1", "role": "primary"},
		Labels: map[string]string{"process": "p1"}}})
	if err != nil || len(out) != 1 || out[0].Key != "sdlc" || out[0].Labels["process"] != "p1" {
		t.Fatalf("put: %+v %v", out, err)
	}
	if _, err := cl.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "execution@Methodology", Value: map[string]any{"name": "sdlc", "role": "boss"}}}); err == nil {
		t.Fatal("a value outside its enum")
	}
	res, err := cl.Submit(ctx, c.ID, graph.Batch{
		Items:   []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"x": 1}}},
		Objects: []domain.ObjectWrite{{Type: "execution@State", Value: map[string]any{"lifecycle": "sdlc", "state": "draft"}}, {Type: "execution@Fact", Value: map[string]any{"title": "f"}}},
	})
	if err != nil || len(res.Items) != 1 || len(res.Objects) != 2 || res.Objects[1].Key != "FACT-1" {
		t.Fatalf("submit: %+v %v", res, err)
	}
	all, err := cl.Objects(ctx, c.ID, domain.ObjectFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("objects: %+v %v", all, err)
	}
	if got, err := cl.Objects(ctx, c.ID, domain.ObjectFilter{Workspaces: []string{""}, Labels: map[string]string{"process": "p1"}}); err != nil || len(got) != 1 {
		t.Fatalf("filtered: %+v %v", got, err)
	}
	if got, err := cl.Objects(ctx, c.ID, domain.ObjectFilter{AtSeq: out[0].Seq}); err != nil || len(got) != 1 {
		t.Fatalf("at a position: %+v %v", got, err)
	}
	entries, _, err := cl.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Labels: map[string]string{"process": "p1"}})
	if err != nil || len(entries) != 1 || entries[0].Labels["process"] != "p1" {
		t.Fatalf("log by label: %+v %v", entries, err)
	}
}

// Writing change objects asks change-object:write on the project of the change, per type.
func TestChangeObjectsAreRoleGated(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	put := func(roles string) error {
		req := connect.NewRequest(&graphv1.PutChangeObjectsRequest{ChangeId: string(c.ID), Objects: []*graphv1.ObjectWrite{{Type: "execution@Fact"}}})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		_, err := h.PutChangeObjects(ctx, req)
		return err
	}
	if err := put(""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("not on the project: %v", err)
	}
	if err := put("developer"); err != nil {
		t.Fatalf("a member of the project: %v", err)
	}
}

// A change object type flagged system (the state of a change, ADR 0098) is written by platform services only: a person
// is refused, an administrator too; the engine, a platform service, writes it.
func TestSystemChangeObjectTypes(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	put := func(subject, roles string) error {
		r := connect.NewRequest(&graphv1.PutChangeObjectsRequest{ChangeId: string(c.ID), Objects: pbconv.ObjectWritesToPB([]domain.ObjectWrite{
			{Type: "execution@State", Value: map[string]any{"lifecycle": "l", "state": "s"}}})})
		r.Header().Set(identity.HeaderSubject, subject)
		r.Header().Set(identity.HeaderRoles, roles)
		_, err := h.PutChangeObjects(ctx, r)
		return err
	}
	for _, roles := range []string{"developer", "admin"} {
		if err := put("alice", roles); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s writes the state by hand: %v", roles, err)
		}
	}
	if err := put(authz.System("engine").Subject, ""); err != nil {
		t.Fatalf("the engine writes the state: %v", err)
	}
}
