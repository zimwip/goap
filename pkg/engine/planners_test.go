package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func plannerTestCompiled(t *testing.T) *methodology.Compiled {
	t.Helper()
	m := &methodology.Methodology{
		Name: "plannertest", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "ready", Expr: "true"}, {Name: "blocked", Expr: "true"}},
		Goals:      []methodology.Goal{{Name: "done", Pre: map[string]bool{"ready": true}}},
		Actions: []methodology.Action{
			{Name: "prep", Kind: methodology.KindHuman, Effects: map[string]bool{"ready": true}, Description: "prepares things"},
			{Name: "other", Kind: methodology.KindHuman, Pre: map[string]bool{"blocked": true}, Effects: map[string]bool{"ready": true}},
		},
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func plannerTestFixtures() ([]goap.Action, goap.Goal, goap.WorldState) {
	actions := []goap.Action{
		{Name: "prep", Effects: map[string]bool{"ready": true}, Cost: 1},
		{Name: "other", Pre: map[string]bool{"blocked": true}, Effects: map[string]bool{"ready": true}, Cost: 1},
	}
	goal := goap.Goal{Name: "done", Pre: map[string]bool{"ready": true}}
	world := goap.WorldState{}
	return actions, goal, world
}

func TestLLMPlannerPicksAction(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"action":"prep"}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLM, Model: "fast"}
	plan, calls, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Name != "prep" {
		t.Fatalf("plan: %+v", plan)
	}
	if len(calls) != 1 || calls[0].Model != "fast" {
		t.Fatalf("calls: %+v", calls)
	}
}

func TestLLMPlannerNoAction(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"action":""}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLM, Model: "fast"}
	_, _, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if !errors.Is(err, goap.ErrNoPlan) {
		t.Fatalf("err: %v", err)
	}
}

func TestLLMPlannerRejectsUnavailableAction(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"action":"nonexistent"}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLM, Model: "fast"}
	_, _, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if !errors.Is(err, ErrPlannerAnswer) {
		t.Fatalf("err: %v", err)
	}
	if errors.Is(err, goap.ErrNoPlan) {
		t.Fatalf("must not be mistaken for ErrNoPlan: %v", err)
	}
}

func TestLLMPlannerRejectsActionWithUnmetPreconditions(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"action":"other"}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLM, Model: "fast"}
	_, _, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if !errors.Is(err, ErrPlannerAnswer) {
		t.Fatalf("err: %v", err)
	}
}

func TestLLMScoringPlannerReweights(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"utilities":{"prep":5}}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLMScoring, Model: "fast"}
	plan, calls, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reweightPlan(goap.Planner{}, world, actions, goal, map[string]float64{"prep": 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != len(want.Actions) || plan.Actions[0].Name != want.Actions[0].Name {
		t.Fatalf("plan %+v want %+v", plan, want)
	}
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
}

func TestLLMScoringPlannerExcludesUnscoredActions(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{LLM: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"utilities":{}}`, Provider: "test", Model: r.Model}, nil
	})}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerLLMScoring, Model: "fast"}
	_, _, err := e.plan(context.Background(), m, ag, world, actions, goal, nil)
	if !errors.Is(err, goap.ErrNoPlan) {
		t.Fatalf("err: %v", err)
	}
}

func TestHybridPlannerUnaffectedByReweightExtraction(t *testing.T) {
	m := plannerTestCompiled(t)
	actions, goal, world := plannerTestFixtures()
	e := &Engine{}
	ag := methodology.Agent{Name: "a", Planner: methodology.PlannerHybrid}
	utilities := map[string]float64{"prep": 2, "other": 5}
	plan, calls, err := e.plan(context.Background(), m, ag, world, actions, goal, utilities)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("hybrid must not call an LLM: %+v", calls)
	}
	want, err := reweightPlan(goap.Planner{}, world, actions, goal, utilities)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != len(want.Actions) || plan.Actions[0].Name != want.Actions[0].Name {
		t.Fatalf("plan %+v want %+v", plan, want)
	}
}
