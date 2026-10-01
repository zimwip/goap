package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// TestPreviewPlanGOAP exercises the plain A* planner (test-designer, goap): from an empty world it chains
// collect_scope -> design_tests -> summarize to reach "designed".
func TestPreviewPlanGOAP(t *testing.T) {
	m := loadMethodology(t, "test-design.yaml")
	p, err := PreviewPlan(m, domain.Blackboard{}, "test-designer", "designed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Reached || p.Planner != methodology.PlannerGOAP || p.Cost != 3 {
		t.Fatalf("%+v", p)
	}
	var got []string
	for _, a := range p.Actions {
		got = append(got, a.Name)
	}
	if want := []string{"collect_scope", "design_tests", "summarize"}; !slices.Equal(got, want) {
		t.Fatalf("plan %v, want %v", got, want)
	}
}

// TestPreviewPlanUtility exercises the greedy utility planner (reviewer): the condition override makes both
// candidate actions admissible, and the utility CEL (read from bb.Vars) picks between them.
func TestPreviewPlanUtility(t *testing.T) {
	m := loadMethodology(t, "test-design.yaml")
	overrides := map[string]bool{"tests_designed": true}

	p, err := PreviewPlan(m, domain.Blackboard{}, "reviewer", "review", overrides)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Name != "auto_review" {
		t.Fatalf("expected auto_review (higher utility by default): %+v", p)
	}

	// a human reviewer is preferred: auto_review's utility drops below human_review's
	p, err = PreviewPlan(m, domain.Blackboard{Vars: map[string]any{"review": "human"}}, "reviewer", "review", overrides)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Name != "human_review" {
		t.Fatalf("expected human_review once vars.review=human: %+v", p)
	}
}

// TestPreviewPlanHybrid exercises the hybrid (utility-weighted A*) planner on the coordinator agent.
func TestPreviewPlanHybrid(t *testing.T) {
	m := loadMethodology(t, "test-design.yaml")
	p, err := PreviewPlan(m, domain.Blackboard{}, "coordinator", "delivered", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Planner != methodology.PlannerHybrid || len(p.Actions) != 1 || p.Actions[0].Name != "delegate" {
		t.Fatalf("%+v", p)
	}
}

// TestPreviewPlanReached: overriding the goal's own conditions true needs no plan.
func TestPreviewPlanReached(t *testing.T) {
	m := loadMethodology(t, "test-design.yaml")
	p, err := PreviewPlan(m, domain.Blackboard{}, "reviewer", "review", map[string]bool{"reviewed": true})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Reached || len(p.Actions) != 0 {
		t.Fatalf("%+v", p)
	}
}

// TestPreviewPlanAwaiting: forcing tests_designed false leaves the reviewer with no admissible action; it is
// waiting on tests_designed (established outside this agent), not stuck.
func TestPreviewPlanAwaiting(t *testing.T) {
	m := loadMethodology(t, "test-design.yaml")
	p, err := PreviewPlan(m, domain.Blackboard{}, "reviewer", "review", map[string]bool{"tests_designed": false})
	if err != nil {
		t.Fatal(err)
	}
	if p.Reached || len(p.Actions) != 0 || !slices.Equal(p.Awaiting, []string{"tests_designed"}) {
		t.Fatalf("%+v", p)
	}
}

// TestPreviewPlanLivePlanner: an llm/llm-scoring planner cannot be previewed (it would call a model live).
func TestPreviewPlanLivePlanner(t *testing.T) {
	m := loadMethodology(t, "sdlc.yaml")
	for _, ag := range m.AgentList() {
		if ag.Planner != methodology.PlannerLLM && ag.Planner != methodology.PlannerLLMScoring {
			continue
		}
		goals := m.AgentGoals(ag)
		if len(goals) == 0 {
			continue
		}
		if _, err := PreviewPlan(m, domain.Blackboard{}, ag.Name, goals[0].Name, nil); !errors.Is(err, ErrLivePlanner) {
			t.Fatalf("agent %s: expected ErrLivePlanner, got %v", ag.Name, err)
		}
		return
	}
	t.Skip("no llm/llm-scoring agent in sdlc.yaml")
}
