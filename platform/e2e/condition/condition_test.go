// Conditions evaluated over the blackboard of a change of a real graph (ADR 0125: pkg/condition is tested without the graph).
package conditione2e

import (
	"context"
	"testing"

	"github.com/zimwip/goap/engine/condition"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
)

func blackboard(t *testing.T, withTest bool) domain.Blackboard {
	t.Helper()
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	need, _ := graphtest.Import(ctx, g, graphtest.Node{Key: "NEED-1", Type: "Need"})
	req, err := graphtest.Import(ctx, g, graphtest.Node{Key: "REQ-1", Type: "Requirement", Links: []graph.LinkWrite{{Type: "satisfies", To: need.Ref()}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := g.BranchHead(ctx, "", domain.MainBranch)
	c, _ := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "c", BaselineID: b.ID})
	ref := req.Ref()
	if _, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "impacted"}}); err != nil {
		t.Fatal(err)
	}
	if withTest {
		if _, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: "TST-9", Type: "TestCase", Rationale: "cover REQ-1", Properties: map[string]any{"title": "t"},
			Links: []graph.LinkWrite{{Type: "verifies", To: ref}}}); err != nil {
			t.Fatal(err)
		}
	}
	bb, err := g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return bb
}

func TestEvaluate(t *testing.T) {
	exp := condition.Expectation{ForEach: "changeImpacts", Where: `x.type == "Requirement"`,
		Produce: condition.ProduceSpec{Op: "create_node", NodeType: "TestCase"}, Link: &condition.LinkSpec{Type: "verifies"}}
	expr, err := exp.Expr()
	if err != nil {
		t.Fatal(err)
	}
	set, err := condition.Compile([]condition.Definition{
		{Name: "has_impacts", Expr: `changeImpacts.exists(n, n.intent == "modified")`},
		{Name: "req_impacted", Expr: `changeImpacts.exists(n, n.type == "Requirement" && n.pre.out.exists(l, l.type == "satisfies" && l.to.key == "NEED-1"))`},
		{Name: "up_to_date", Expr: "changeImpacts.all(n, n.pre == null || n.pre.version == n.pre.latest)"},
		{Name: "tests_proposed", Expr: expr},
		{Name: "broken", Expr: "changeImpacts[0].nope == 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := set.Evaluate(blackboard(t, false))
	want := map[string]bool{"has_impacts": true, "req_impacted": true, "up_to_date": true, "tests_proposed": false}
	for k, v := range want {
		if got, ok := res.State[k]; !ok || got != v {
			t.Errorf("%s: got %v (known=%v) want %v; errors=%v", k, got, ok, v, res.Errors)
		}
	}
	if _, ok := res.State["broken"]; ok || res.Errors["broken"] == "" {
		t.Errorf("broken condition must be unknown")
	}
	res = set.Evaluate(blackboard(t, true))
	if !res.State["tests_proposed"] {
		t.Errorf("tests_proposed should hold: %v", res.Errors)
	}
}

func TestChangeImpacts(t *testing.T) {
	set, err := condition.Compile([]condition.Definition{
		{Name: "one_planned", Expr: `changeImpacts.filter(n, n.intent == "modified" && n.planned && n.pre.key == "REQ-1").size() == 1`},
		{Name: "created_test", Expr: `changeImpacts.exists(n, n.intent == "created" && n.type == "TestCase" && n.pre == null && n.hasPost)`},
		{Name: "all_reviewed", Expr: `changeImpacts.all(n, n.review != "proposed")`},
		{Name: "linked", Expr: `changeImpacts.exists(n, n.pre != null && n.pre.out.exists(l, l.type == "satisfies"))`},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := set.Evaluate(blackboard(t, true))
	want := map[string]bool{"one_planned": true, "created_test": true, "all_reviewed": false, "linked": true}
	for k, v := range want {
		if got, ok := res.State[k]; !ok || got != v {
			t.Errorf("%s: got %v (known=%v) want %v; errors=%v", k, got, ok, v, res.Errors)
		}
	}
}
