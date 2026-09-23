package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func loadMethodology(t *testing.T, file string) *methodology.Compiled {
	t.Helper()
	data, err := os.ReadFile("../../methodologies/" + file)
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// agentsSetup extends the impact-analysis setup with test-design, scripts
// run in-process and a fake model gateway.
func agentsSetup(t *testing.T) (*Engine, domain.BaselineID) {
	t.Helper()
	e, _, base := setup(t)
	statics := e.Methodologies.(StaticMethodologies)
	statics["test-design"] = loadMethodology(t, "test-design.yaml")
	e.Executors[methodology.KindScript] = ScriptExecutor{Sandboxes: InprocSandboxes{}}
	e.LLM = llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: "Campagne de test.", Provider: "test", Model: r.Model, Usage: llm.Usage{InputTokens: 42, OutputTokens: 7}}, nil
	})
	e.Schedule = func(id string) { e.background(func() { _, _ = e.Run(context.Background(), id) }) }
	return e, base
}

func TestIdentifyAgentAcrossMethodologies(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{BaselineID: base, Intent: "préparer et valider la campagne de tests"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Methodology != "test-design" || p.Agent != "coordinator" || p.Goal != "delivered" || p.Planner != "hybrid" {
		t.Fatalf("wrong identification: %s/%s/%s (%s) %+v", p.Methodology, p.Agent, p.Goal, p.Status, p.Candidates)
	}
	if p.Candidates[0].Agent != "coordinator" || p.Candidates[0].Methodology != "test-design" {
		t.Fatalf("candidates not qualified: %+v", p.Candidates[0])
	}
}

func TestScriptAgentWithLLMUsage(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, Intent: "concevoir les cas de test"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.Run(ctx, p.ID)
	if err != nil || p.Status != StatusCompleted {
		t.Fatalf("status %s %s %v steps=%+v", p.Status, p.Error, err, p.Steps)
	}
	var actions []string
	for _, s := range p.Steps {
		actions = append(actions, s.Action)
	}
	if strings.Join(actions, ",") != "collect_scope,design_tests,summarize" {
		t.Fatalf("steps %v", actions)
	}
	sum := p.Steps[2]
	if len(sum.LLMCalls) != 1 || sum.Usage.InputTokens != 42 || sum.LLMCalls[0].Model != "fast" || sum.Sandbox != "inproc" {
		t.Fatalf("llm usage not recorded: %+v", sum)
	}
	if p.Usage.InputTokens != 42 || p.Usage.OutputTokens != 7 || p.Usage.LLMCalls != 1 {
		t.Fatalf("process usage %+v", p.Usage)
	}
	if len(p.Steps[0].Logs) != 1 || !strings.Contains(p.Steps[0].Logs[0].Message, "exigence") {
		t.Fatalf("script logs not recorded: %+v", p.Steps[0].Logs)
	}
}

func TestSubAgentsWithSuspension(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "coordinator", BaselineID: base,
		Intent: "préparer et valider la campagne de tests", Vars: map[string]any{"review": "human"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	// the reviewer (utility planner) prefers the human review: the coordinator is suspended
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent || p.Pending.ChildProcessID == "" {
		t.Fatalf("expected suspension on a sub-agent, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	reviewer, _ := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if reviewer.Agent != "reviewer" || reviewer.ParentID != p.ID || reviewer.Status != StatusWaiting || reviewer.Pending.Action != "human_review" {
		t.Fatalf("unexpected reviewer %+v", reviewer)
	}
	if len(p.Steps[0].Children) != 2 {
		t.Fatalf("children not recorded on the step: %+v", p.Steps[0])
	}
	// the human accepts every proposal of the shared change
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	var decisions []ItemInput
	for _, it := range bb.Change.ItemsOfKind(domain.KindProposal) {
		decisions = append(decisions, ItemInput{Kind: "decision", Decision: &DecisionInput{Item: "@" + string(it.ID), Accept: true}})
	}
	if _, err := e.Submit(ctx, reviewer.ID, decisions); err != nil {
		t.Fatal(err)
	}
	e.schedule(reviewer.ID)
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("coordinator not resumed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if len(p.Children) != 0 {
		t.Fatalf("children map must be cleared after the action: %v", p.Children)
	}
	designer, _ := e.Store.Get(ctx, p.Steps[len(p.Steps)-1].Children[0])
	if designer.Agent != "test-designer" || designer.Usage.LLMCalls != 1 {
		t.Fatalf("designer child %+v", designer)
	}
}

func TestUtilityPlannerAutoReview(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	d, _ := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, Intent: "concevoir"})
	d, _ = e.Run(ctx, d.ID)
	r, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "reviewer", ChangeID: d.ChangeID, Intent: "revoir"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = e.Run(ctx, r.ID)
	if r.Status != StatusCompleted || r.Steps[0].Action != "auto_review" {
		t.Fatalf("utility planner should pick auto_review: %s %+v", r.Status, r.Steps)
	}
}
