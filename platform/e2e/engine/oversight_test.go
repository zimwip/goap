package enginee2e

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/dsl"
)

// The oversight floor (EU AI Act Art. 14): with PersonReviewsModelOutput, a model accepts nothing and an automated
// action accepts no impact a model produced; a person's verdict and an automated one on what no model produced stand.
func TestPersonReviewsModelOutput(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	e.PersonReviewsModelOutput = true
	in := engine.InternalsOf(e)
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Namespace: "alm", Title: "PSP v2", ForkCommit: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	p := &engine.Process{ChangeID: c.ID}
	llm := in.WithAutomated(ctx, methodology.Action{Name: "draft", Kind: methodology.KindLLM})
	script := in.WithAutomated(ctx, methodology.Action{Name: "check", Kind: methodology.KindScript})

	// a model writes REQ-1 (its run called a model) and may not accept it
	if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: "model.call", Execution: "exec-llm"}}); err != nil {
		t.Fatal(err)
	}
	write := []dsl.NodeOp{{Op: "declare", Ref: "#n1", Intent: string(domain.IntentModified), Key: "REQ-1", Rationale: "PSP v2 changes the payment API"},
		{Op: "write", Node: "#n1", Props: map[string]any{"title": "Use PSP v2"}}}
	if _, err := in.ApplyNodeOps(llm, p, write, "draft", "exec-llm"); err != nil {
		t.Fatal(err)
	}
	accept := []dsl.NodeOp{{Op: "review", Node: "REQ-1", Accept: true, Comment: "fine"}}
	if _, err := in.ApplyNodeOps(llm, p, accept, "draft", "exec-llm"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a model accepting: %v", err)
	}
	// nor may a script accept what the model produced
	if _, err := in.ApplyNodeOps(script, p, accept, "check", "exec-script"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a script accepting a model's output: %v", err)
	}
	// a script accepts what it produced itself
	own := []dsl.NodeOp{{Op: "declare", Ref: "#n1", Intent: string(domain.IntentCreated), Key: "TST-2", Type: "TestCase", Rationale: "cover the new API"},
		{Op: "write", Node: "#n1", Props: map[string]any{"title": "PSP v2 test"}}, {Op: "review", Node: "#n1", Accept: true, Comment: "recorded"}}
	if _, err := in.ApplyNodeOps(script, p, own, "check", "exec-script"); err != nil {
		t.Fatalf("a script accepting its own output: %v", err)
	}
	// a person accepts the model's output
	if _, err := in.ApplyNodeOps(ctx, p, accept, "review_by_hand", "exec-human"); err != nil {
		t.Fatalf("a person accepting: %v", err)
	}
	ch, err := g.Change(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ch.Nodes {
		if n.Review != domain.ReviewAccepted {
			t.Fatalf("%s is %s", n.Key, n.Review)
		}
	}
}
