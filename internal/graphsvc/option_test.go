package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// The options of a change through the service (ADR 0032 §6): the client the engine and the built-in MCPs use in the
// services round-trips what the in-process graph gives.
func TestOptionsThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphsvc.NewClient(srv.Client(), srv.URL)

	req1, err := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.CreateBaseline(ctx, "", "B1", []domain.NodeRef{req1.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := cl.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "a", Hypothesis: "h", Activate: true})
	if err != nil || a.Option == nil || a.Option.Name != "a" || !a.Active {
		t.Fatalf("open: %+v %v", a, err)
	}
	pre := req1.Ref()
	added, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "why"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteNode(ctx, c.ID, added[0].ID, graph.NodeWrite{Properties: map[string]any{"title": "two"}}); err != nil {
		t.Fatal(err)
	}
	nodes, _, err := cl.ChangeGraph(ctx, c.ID, "")
	if err != nil || len(nodes) != 1 || nodes[0].Properties["title"] != "two" {
		t.Fatalf("change graph on the active option: %+v %v", nodes, err)
	}
	if v, err := cl.ChangeView(ctx, c.ID, domain.MainFlow, graph.ViewWritten); err != nil || !v.Contains(pre) {
		t.Fatalf("main view: %+v %v", v, err)
	}
	cmp, err := cl.CompareOptions(ctx, c.ID, "", false)
	if err != nil || len(cmp.Nodes) != 1 || cmp.Nodes[0].Options[a.ID] == nil || cmp.Nodes[0].Props[a.ID]["title"] != "two" {
		t.Fatalf("compare: %+v %v", cmp, err)
	}
	if f, err := cl.EvaluateOption(ctx, c.ID, a.ID, "", "good"); err != nil || f.Evaluation != "good" || !f.Evaluated {
		t.Fatalf("evaluate: %+v %v", f, err)
	}
	if active, err := cl.ActivateOption(ctx, c.ID, domain.MainFlow, ""); err != nil || active != "" {
		t.Fatalf("deactivate: %q %v", active, err)
	}
	if os, err := cl.Options(ctx, c.ID); err != nil || len(os) != 1 || os[0].Active {
		t.Fatalf("options: %+v %v", os, err)
	}
	vs, err := cl.ChangeView(ctx, c.ID, a.ID, graph.ViewWritten)
	if err != nil || vs.Contains(pre) {
		t.Fatalf("option view: %+v %v", vs, err)
	}
}
