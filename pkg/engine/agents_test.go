package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func loadMethodology(t *testing.T, file string) *methodology.Compiled {
	t.Helper()
	path := "../../methodologies/" + file
	if _, err := os.Stat(path); err != nil { // the examples are not seeded at start: they live in methodologies/examples
		path = "../../methodologies/examples/" + file
	}
	m, err := methodology.LoadFile(path)
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
		return llm.Response{Text: "Test campaign.", Provider: "test", Model: r.Model, Usage: llm.Usage{InputTokens: 42, OutputTokens: 7}}, nil
	})
	e.Schedule = func(id string) { e.background(func() { _, _ = e.Run(context.Background(), id) }) }
	return e, base
}

func TestIdentifyAgentAcrossMethodologies(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	// the intent text deliberately echoes the coordinator's example phrase
	// in methodologies/examples/test-design.yaml.
	p, err := e.Start(ctx, StartRequest{BaselineID: base, ProjectID: testProject, Intent: "prepare and validate the test campaign"})
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
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, ProjectID: testProject, Intent: "design the test cases"})
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
	// "requirement" is logged by the collect_scope script in
	// methodologies/examples/test-design.yaml.
	if len(p.Steps[0].Logs) != 1 || !strings.Contains(p.Steps[0].Logs[0].Message, "requirement") {
		t.Fatalf("script logs not recorded: %+v", p.Steps[0].Logs)
	}
}

func TestSubAgentsWithSuspension(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "coordinator", BaselineID: base, ProjectID: testProject,
		Intent: "prepare and validate the test campaign", Vars: map[string]any{"review": "human"}})
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
	// the human accepts every test case of the shared change
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	var decisions []ItemInput
	for _, n := range bb.Change.Nodes {
		if n.Intent == domain.IntentCreated {
			decisions = append(decisions, ItemInput{Kind: "changeImpact", ChangeImpact: &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "reviewed by a human"}})
		}
	}
	if len(decisions) == 0 {
		t.Fatalf("no test case to review: %+v", bb.Change.Nodes)
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

func TestSubAgentWakesParentOnSignal(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "watcher", BaselineID: base, ProjectID: testProject, Intent: "watch a pinger and wake on its signal"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskAgent || p.Pending.ChildProcessID == "" {
		t.Fatalf("expected the watcher suspended on the pinger, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if len(p.Pending.WakeOn) != 1 || p.Pending.WakeOn[0] != "progress" {
		t.Fatalf("wakeOn not recorded on the pending task: %+v", p.Pending)
	}
	child := p.Pending.ChildProcessID
	pinger, _ := e.Store.Get(ctx, child)
	if pinger.Agent != "pinger" || pinger.ParentID != p.ID || pinger.Status != StatusWaiting {
		t.Fatalf("unexpected pinger %+v", pinger)
	}
	stepsBeforeSignal := len(p.Steps)

	// let the pinger send its progress signal, without letting it terminate
	if _, err := e.Submit(ctx, child, []ItemInput{{Kind: "artifact", Type: "go"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(child)
	e.Drain()

	pinger, _ = e.Store.Get(ctx, child)
	if pinger.Status.Terminal() {
		t.Fatalf("pinger must still be running (not terminated) when it signals, got %s", pinger.Status)
	}
	if pinger.Status != StatusWaiting {
		t.Fatalf("pinger expected waiting again after signaling: %s %+v", pinger.Status, pinger.Error)
	}

	// the watcher must have been woken and replayed wait_for_ping BEFORE the
	// pinger terminated (the old behavior only wakes a parent at child
	// termination): a new step, and a fresh suspension on the same child.
	p, _ = e.Store.Get(ctx, p.ID)
	if len(p.Steps) != stepsBeforeSignal+1 || p.Steps[len(p.Steps)-1].Action != "wait_for_ping" {
		t.Fatalf("watcher was not woken early by the signal (steps before=%d after=%d): %+v", stepsBeforeSignal, len(p.Steps), p.Steps)
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.ChildProcessID != child {
		t.Fatalf("watcher must have re-suspended on the still-running pinger: %s %+v", p.Status, p.Pending)
	}

	// now let the pinger actually finish, and the watcher complete
	if _, err := e.Submit(ctx, child, []ItemInput{{Kind: "artifact", Type: "wrapped"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(child)
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("watcher not completed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
}

// TestChildVarsAreIsolated (gap 4): a sub-agent's Vars are cloned at spawn, so
// a mutation on either side (SetVar) is not visible to the other.
func TestChildVarsAreIsolated(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "watcher", BaselineID: base, ProjectID: testProject,
		Intent: "watch a pinger and wake on its signal", Vars: map[string]any{"shared": "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending == nil {
		t.Fatalf("expected the watcher suspended on the pinger, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	child := p.Pending.ChildProcessID
	pinger, _ := e.Store.Get(ctx, child)
	if pinger.Vars["shared"] != "parent" {
		t.Fatalf("child did not inherit the parent's vars at spawn: %+v", pinger.Vars)
	}

	// the pinger's own ping/wait_ready scripts set vars ("pinged"); the parent
	// must not see them, and the parent's own var must stay untouched by the child.
	if _, err := e.Submit(ctx, child, []ItemInput{{Kind: "artifact", Type: "go"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(child)
	e.Drain()

	pinger, _ = e.Store.Get(ctx, child)
	if pinger.Vars["pinged"] != true {
		t.Fatalf("child's own SetVar was not recorded: %+v", pinger.Vars)
	}
	if pinger.Vars["shared"] != "parent" {
		t.Fatalf("child lost the inherited var: %+v", pinger.Vars)
	}

	p, _ = e.Store.Get(ctx, p.ID)
	if _, ok := p.Vars["pinged"]; ok {
		t.Fatalf("child's SetVar leaked into the parent's vars: %+v", p.Vars)
	}
	if p.Vars["shared"] != "parent" {
		t.Fatalf("parent's own var was affected by the child: %+v", p.Vars)
	}
}

func TestUtilityPlannerAutoReview(t *testing.T) {
	ctx := context.Background()
	e, base := agentsSetup(t)
	d, _ := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, ProjectID: testProject, Intent: "design"})
	d, _ = e.Run(ctx, d.ID)
	r, err := e.Start(ctx, StartRequest{Methodology: "test-design", Agent: "reviewer", ChangeID: d.ChangeID, Intent: "review"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = e.Run(ctx, r.ID)
	if r.Status != StatusCompleted || r.Steps[0].Action != "auto_review" {
		t.Fatalf("utility planner should pick auto_review: %s %+v", r.Status, r.Steps)
	}
}
