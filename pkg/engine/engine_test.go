package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// scripted answers by prompt content, like a deterministic LLM.
func scripted(t *testing.T) llm.Client {
	return llm.ClientFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		var out string
		switch {
		case strings.Contains(p, "DIRECTEMENT impactés"):
			if !strings.Contains(p, "REQ-1 (Requirement)") {
				t.Errorf("prompt misses baseline nodes:\n%s", p)
			}
			out = `Voici: {"items":[{"kind":"impact","type":"direct","target":"REQ-1","data":{"reason":"PSP API change"}}]}`
		case strings.Contains(p, "nouvelle version"):
			out = `{"items":[{"kind":"proposal","proposal":{"op":"update_node","node":{"base":"REQ-1","props":{"title":"Use PSP v2"}}}}]}`
		case strings.Contains(p, "cas de test"):
			out = `{"items":[{"ref":"t1","kind":"proposal","proposal":{"op":"create_node","node":{"key":"TST-9","type":"TestCase"}}},
			{"kind":"proposal","proposal":{"op":"add_link","link":{"type":"verifies","from":"#t1","to":"REQ-1"}}}]}`
		case strings.Contains(p, "rapport"):
			out = `{"items":[{"kind":"artifact","type":"report","data":{"markdown":"# Impact"}}]}`
		default:
			t.Errorf("unexpected prompt %s", p)
		}
		return llm.Response{Text: out, Provider: "test", Model: req.Model}, nil
	})
}

func setup(t *testing.T) (*Engine, *graph.Graph, domain.BaselineID) {
	t.Helper()
	ctx := context.Background()
	data, err := os.ReadFile("../../methodologies/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	need, _ := g.CreateNode(ctx, graph.NewNode{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "Pay online"}})
	req, _ := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "Use PSP v1"}})
	tst, _ := g.CreateNode(ctx, graph.NewNode{Key: "TST-1", Type: "TestCase"})
	cmp, _ := g.CreateNode(ctx, graph.NewNode{Key: "CMP-1", Type: "Component"})
	_, _ = g.Link(ctx, "satisfies", req.Ref(), need.Ref(), nil)
	_, _ = g.Link(ctx, "verifies", tst.Ref(), req.Ref(), nil)
	_, _ = g.Link(ctx, "implements", cmp.Ref(), req.Ref(), nil)
	b, err := g.CreateBaseline(ctx, "B1", []domain.NodeRef{need.Ref(), req.Ref(), tst.Ref(), cmp.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	client := scripted(t)
	e := &Engine{
		Graph:         g,
		Methodologies: StaticMethodologies{cm.Name: cm},
		Executors: map[string]Executor{
			methodology.KindLLM:     LLMExecutor{Client: client},
			methodology.KindHuman:   HumanExecutor{},
			methodology.KindBuiltin: DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  NewMemoryStore(),
	}
	return e, g, b.ID
}

func TestAssessImpact(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "Le PSP change d'API, qu'est-ce que ça casse ?"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusRunning || p.Goal != "assess_impact" {
		t.Fatalf("intent not resolved: %+v", p)
	}
	p, err = e.Run(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusCompleted {
		t.Fatalf("status %s (%s) world=%v unknown=%v steps=%+v", p.Status, p.Error, p.World, p.Unknown, p.Steps)
	}
	var actions []string
	for _, s := range p.Steps {
		actions = append(actions, s.Action)
	}
	if strings.Join(actions, ",") != "identify_impacts,propagate_impacts,write_report" {
		t.Fatalf("unexpected steps %v", actions)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	impacts := c.ItemsOfKind(domain.KindImpact)
	// REQ-1 direct, TST-1 and CMP-1 propagated (NEED-1 is upstream, not impacted)
	if len(impacts) != 3 {
		t.Fatalf("expected 3 impacts, got %d", len(impacts))
	}
	if c.Goal != "assess_impact" {
		t.Fatalf("goal not recorded on the change")
	}
}

func TestPrepareChangeWithClarificationAndReview(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "Le fournisseur de paiement change"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusClarifying || p.Question == "" {
		t.Fatalf("expected a clarification, got %+v", p)
	}
	p, err = e.Answer(ctx, p.ID, "prepare_change")
	if err != nil {
		t.Fatal(err)
	}
	if p.Goal != "prepare_change" {
		t.Fatalf("got goal %q", p.Goal)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Action != "review_proposals" {
		t.Fatalf("expected review task, got %s %+v %s steps=%+v", p.Status, p.Pending, p.Error, p.Steps)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	var decisions []ItemInput
	for _, it := range c.ItemsOfKind(domain.KindProposal) {
		decisions = append(decisions, ItemInput{Kind: "decision", Decision: &DecisionInput{Item: "@" + string(it.ID), Accept: true}})
	}
	if _, err := e.Submit(ctx, p.ID, decisions); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("status %s %s world=%v", p.Status, p.Error, p.World)
	}
	b2, err := g.Apply(ctx, p.ChangeID, "B2")
	if err != nil {
		t.Fatal(err)
	}
	nodes, links, _ := g.BaselineGraph(ctx, b2.ID)
	if len(nodes) != 5 {
		t.Fatalf("expected 5 nodes in target graph, got %d", len(nodes))
	}
	found := false
	for _, l := range links {
		if l.Type == "verifies" && l.To.Version == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("new test must verify REQ-1@v2: %+v", links)
	}
}

func TestStuckWhenNoPlan(t *testing.T) {
	ctx := context.Background()
	e, _, base := setup(t)
	// make the LLM useless: identify_impacts never delivers, gets disabled,
	// then the human fallback is planned.
	e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"items":[]}`}, nil
	})}
	p, _ := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Goal: "assess_impact"})
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending.Action != "select_impacts" {
		t.Fatalf("expected fallback to human selection, got %s %+v", p.Status, p.Pending)
	}
	if !p.Disabled["identify_impacts"] {
		t.Fatal("identify_impacts should be disabled")
	}
}
