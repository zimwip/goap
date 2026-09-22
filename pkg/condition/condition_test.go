package condition

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

func blackboard(t *testing.T, withProposal bool) domain.Blackboard {
	t.Helper()
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	need, _ := g.CreateNode(ctx, graph.NewNode{Key: "NEED-1", Type: "Need"})
	req, _ := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement"})
	if _, err := g.Link(ctx, "satisfies", req.Ref(), need.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	b, _ := g.CreateBaseline(ctx, "B1", []domain.NodeRef{need.Ref(), req.Ref()})
	c, _ := g.CreateChange(ctx, graph.NewChange{Title: "c", BaselineID: b.ID})
	ref := req.Ref()
	items, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindImpact, Type: "direct", Target: &ref}})
	if err != nil {
		t.Fatal(err)
	}
	if withProposal {
		created, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, DerivedFrom: []domain.ItemID{items[0].ID},
			Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: "TST-9", Type: "TestCase"}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
			Link: &domain.LinkDraft{Type: "verifies", From: domain.Endpoint{Item: created[0].ID}, To: domain.Endpoint{Node: &ref}}}}}); err != nil {
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
	exp := Expectation{ForEach: "impacts", Where: `x.target.type == "Requirement"`,
		Produce: ProduceSpec{Op: "create_node", NodeType: "TestCase"}, Link: &LinkSpec{Type: "verifies"}}
	expr, err := exp.Expr()
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile([]Definition{
		{Name: "has_impacts", Expr: "size(impacts) > 0"},
		{Name: "req_impacted", Expr: `impacts.exists(i, i.target.type == "Requirement" && i.target.out.exists(l, l.type == "satisfies" && l.to.key == "NEED-1"))`},
		{Name: "up_to_date", Expr: "impacts.all(i, i.target.version == i.target.latest)"},
		{Name: "tests_proposed", Expr: expr},
		{Name: "broken", Expr: "impacts[0].nope == 1"},
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

func TestCompileErrors(t *testing.T) {
	if _, err := Compile([]Definition{{Name: "x", Expr: "size(impacts)"}}); err == nil {
		t.Fatal("non bool expression must be rejected")
	}
	if _, err := Compile([]Definition{{Name: "x", Expr: "unknown_var"}}); err == nil {
		t.Fatal("unknown variable must be rejected")
	}
}
