package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

// The decision loop end to end (ADR 0009 §4, methodologies/examples/option-decision.yaml): the decider opens a
// decision point on the options, rules it undecidable with a question, the question is investigated by a sub-agent
// that identification picks from the question, and the decider comes back to decide: the option is selected.
func TestDecisionLoopEndToEnd(t *testing.T) {
	ctx := context.Background()
	m, err := methodology.LoadFile("../../methodologies/examples/option-decision.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	_, _ = g.CreateNode(ctx, graph.NewNode{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement", Properties: map[string]any{"title": "Pay online"}})
	b, err := g.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "PSP", Intent: "choose a payment provider", Namespace: "alm", BaselineID: b.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	stripe, _ := g.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "stripe", Hypothesis: "Stripe covers every market"})
	if _, err := g.OpenOption(ctx, c.ID, graph.OpenOptionRequest{Name: "adyen", Hypothesis: "Adyen is cheaper"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Graph:         g,
		Methodologies: StaticMethodologies{cm.Name: cm},
		Executors: map[string]Executor{
			methodology.KindScript:  ScriptExecutor{Sandboxes: InprocSandboxes{}},
			methodology.KindBuiltin: DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  NewMemoryStore(),
	}
	p, err := e.Start(ctx, StartRequest{Methodology: cm.Name, Agent: "decider", Goal: "decide_option", ChangeID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range p.Steps {
		steps = append(steps, s.Action)
	}
	if p.Status != StatusCompleted {
		t.Fatalf("status %s (%s) world=%v unknown=%v steps=%v", p.Status, p.Error, p.World, p.Unknown, steps)
	}
	if got := strings.Join(steps, ","); got != "open_decision,decide,investigate,decide" {
		t.Fatalf("steps %s", got)
	}
	points, err := g.DecisionPoints(ctx, c.ID)
	if err != nil || len(points) != 1 {
		t.Fatalf("points: %+v %v", points, err)
	}
	d := points[0]
	if d.Status != domain.PointDecided || d.Option != stripe.ID || d.Rounds != 1 || len(d.Questions) != 1 {
		t.Fatalf("decision: %+v", d)
	}
	q := d.Questions[0]
	if q.Status != domain.QuestionAnswered || q.Process == "" {
		t.Fatalf("the question is answered by the investigation: %+v", q)
	}
	child, err := e.Store.Get(ctx, q.Process)
	if err != nil || child.Agent != "analyst" || child.Status != StatusCompleted || child.ParentID != p.ID {
		t.Fatalf("investigation: %+v %v", child, err)
	}
	opts, _ := g.Options(ctx, c.ID)
	if opts[0].OptionStatus() != domain.OptionSelected || opts[1].OptionStatus() != domain.OptionRejected {
		t.Fatalf("options: %+v", opts)
	}
}

// A ruling below the threshold waits for a person; an agent may not ratify it, a human task may.
func TestDecisionRatificationIsHuman(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	_, _ = g.CreateNode(ctx, graph.NewNode{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement"})
	b, _ := g.BranchHead(ctx, "alm", domain.MainBranch)
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "x", Namespace: "alm", BaselineID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Graph: g}
	p := &Process{ID: "p", ChangeID: c.ID}
	pts, err := e.applyDecisionOps(ctx, p, []DecisionOp{
		{Op: "open", Ref: "#d", Question: "Go?", Threshold: 0.8},
		{Op: "rule", Point: "#d", Outcome: domain.OutcomeDecided, Confidence: 0.5, Justification: "probably"},
	}, false, "decider")
	if err != nil || len(pts) != 1 {
		t.Fatalf("open and rule: %v %v", pts, err)
	}
	if _, err := e.applyDecisionOps(ctx, p, []DecisionOp{{Op: "ratify", Point: pts[0], Accept: true}}, false, "decider"); err == nil {
		t.Fatal("an agent ratified a ruling")
	}
	if _, err := e.applyDecisionOps(ctx, p, []DecisionOp{{Op: "ratify", Point: pts[0], Accept: true}}, true, "ann"); err != nil {
		t.Fatal(err)
	}
	if d, _ := g.DecisionPoints(ctx, c.ID); d[0].Status != domain.PointDecided || d[0].DecidedBy != "ann" {
		t.Fatalf("ratified: %+v", d[0])
	}
}
