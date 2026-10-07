package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
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

	req1, err := graphtest.Import(ctx, g, graphtest.Node{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", BaselineID: base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := cl.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "a", Hypothesis: "h", Activate: true})
	if err != nil || a.Option == nil || a.Option.Name != "a" || !a.Active {
		t.Fatalf("open: %+v %v", a, err)
	}
	pre := req1.Ref()
	added, err := g.ImpactNodeCheckout(ctx, c.ID, graph.NodeCheckout{Node: pre.ID, Rationale: "why"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, added.ID, graph.NodeUpdate{Properties: map[string]any{"title": "two"}}); err != nil {
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
	df, err := cl.DiffFlows(ctx, c.ID, "main", a.ID, "")
	if err != nil || len(df.Impacts) != 1 || df.Impacts[0].Category != domain.DiffAdded || df.Impacts[0].Key != "REQ-1" || df.Right != a.ID {
		t.Fatalf("diff flows: %+v %v", df, err)
	}
	df, err = cl.DiffFlows(ctx, c.ID, a.ID, "main", "")
	if err != nil || len(df.Impacts) != 1 || df.Impacts[0].Category != domain.DiffRemoved || df.Impacts[0].Left == nil || df.Impacts[0].Left.Impact != added.ID {
		t.Fatalf("diff flows reversed: %+v %v", df, err)
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

// Decision points through the service (ADR 0009 §4): the engine's client opens, rules and answers them, the
// blackboard carries them with the options, and a person ratifies through the API.
func TestDecisionsThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	g.DecisionPolicy = decision.Policy{}
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphsvc.NewClient(srv.Client(), srv.URL)

	_, _ = graphtest.Import(ctx, g, graphtest.Node{Key: "REQ-1", Type: "Requirement"})
	base, _ := g.BranchHead(ctx, "", domain.MainBranch)
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "t", BaselineID: base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := g.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "a"})
	g.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "b"}) //nolint:errcheck
	d, err := cl.OpenDecision(ctx, c.ID, graph.OpenDecisionRequest{Question: "a or b?", Criteria: []string{"cost"}, Policy: map[string]any{decision.KeyMaxDuration: "48h"}})
	if err != nil || len(d.Options) != 2 || d.Policy[decision.KeyDeadline] == nil || d.Criteria[0] != "cost" {
		t.Fatalf("open: %+v %v", d, err)
	}
	d, err = cl.RuleDecision(ctx, c.ID, graph.RuleRequest{Point: d.ID, Outcome: domain.OutcomeUndecidable, Justification: "?", Questions: []string{"cost of a?"}})
	if err != nil || d.Status != domain.PointBlocked || len(d.Questions) != 1 {
		t.Fatalf("undecidable: %+v %v", d, err)
	}
	bb, err := cl.BlackboardIn(ctx, c.ID, "")
	if err != nil || len(domain.DecisionPointsOf(bb)) != 1 || len(domain.OptionsOf(bb)) != 2 || bb.At.IsZero() {
		t.Fatalf("blackboard: %+v %+v %v", domain.DecisionPointsOf(bb), domain.OptionsOf(bb), err)
	}
	if _, err := cl.AnswerQuestion(ctx, c.ID, d.Questions[0].ID, "cheap", "", ""); err != nil {
		t.Fatal(err)
	}
	// the engine's rulings are an agent's: below the threshold, a person ratifies
	d, err = cl.RuleDecision(ctx, c.ID, graph.RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Option: "a", Confidence: 0.4, Justification: "cheaper"})
	if err != nil || d.Status != domain.PointRatifying || d.Ruling == nil || d.Ruling.Human {
		t.Fatalf("agent ruling: %+v %v", d, err)
	}
	if d, err = cl.RatifyDecision(ctx, c.ID, d.ID, true, "", "ok"); err != nil || d.Status != domain.PointDecided || d.Option != a.ID {
		t.Fatalf("ratified: %+v %v", d, err)
	}
	if ds, err := cl.DecisionPoints(ctx, c.ID); err != nil || len(ds) != 1 || ds[0].Status != domain.PointDecided {
		t.Fatalf("list: %+v %v", ds, err)
	}
	// the facts of the decision come back as items with their events
	ch, err := g.Change(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range ch.Items {
		if it.Kind == domain.KindDecisionPoint && it.DecisionEvent != nil {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("%d decision events", n)
	}
}
