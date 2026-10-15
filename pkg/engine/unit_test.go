package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func TestWaitingIsToldFromStuck(t *testing.T) {
	e := &Engine{}
	write := goap.Action{Name: "write", Effects: map[string]bool{"written": true}, Cost: 1}
	release := goap.Action{Name: "release", Pre: map[string]bool{"written": true, "approved": true, "frozen": false}, Effects: map[string]bool{"released": true}, Cost: 1}
	goal := goap.Goal{Name: "shipped", Pre: map[string]bool{"released": true}}
	all := []goap.Action{write, release}
	world := goap.WorldState{"frozen": true}

	// approved and !frozen are established outside the agent: it waits for them
	if got := e.awaited(world, all, all, goal); !slices.Equal(got, []string{"!frozen", "approved"}) {
		t.Fatalf("awaited %v", got)
	}
	// once they hold, nothing is awaited any more (the planner finds a plan)
	if got := e.awaited(goap.WorldState{"approved": true, "frozen": false}, all, all, goal); got != nil {
		t.Fatalf("nothing to wait for: %v", got)
	}
	// the agent's own action that would establish what is missing is not admissible (disabled, unbound): stuck
	if got := e.awaited(goap.WorldState{"approved": true, "frozen": false}, all, []goap.Action{release}, goal); got != nil {
		t.Fatalf("stuck, not waiting: %v", got)
	}
	// what is established outside would not be enough: stuck
	if got := e.awaited(world, all, []goap.Action{release}, goal); got != nil {
		t.Fatalf("stuck, not waiting: %v", got)
	}
}

func TestDecisionAsks(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	points := []domain.DecisionPoint{
		{ID: "d1", Question: "Which option?", Status: domain.PointBlocked, OpenedAt: at,
			Questions: []domain.Question{{ID: "q1", Point: "d1", Text: "What is the budget?", Status: domain.QuestionOpen, AskedAt: at.Add(time.Hour)},
				{ID: "q0", Point: "d1", Text: "Answered", Status: "answered"}}},
		{ID: "d2", Question: "Go or no go?", Status: domain.PointEscalated, Escalation: "too many rounds", OpenedAt: at},
		{ID: "d3", Question: "Agents rule it", Status: domain.PointOpen, OpenedAt: at},
		{ID: "d4", Question: "Done", Status: domain.PointDecided, OpenedAt: at},
	}
	asks := decisionAsks(points)
	if len(asks) != 2 {
		t.Fatalf("a question and an escalated point: %+v", asks)
	}
	if a := asks[0]; a.Kind != AskQuestion || a.Question != "q1" || a.Point != "d1" || a.Title != "What is the budget?" || !a.Since.Equal(at.Add(time.Hour)) {
		t.Fatalf("question: %+v", a)
	}
	if a := asks[1]; a.Kind != AskDecision || a.Point != "d2" || a.Detail != "too many rounds" {
		t.Fatalf("decision: %+v", a)
	}
}

// The brief of a step tells the agent instance its role and that it starts fresh (ADR 0050).
func TestStepSectionTellsRoleAndFreshStart(t *testing.T) {
	sc := &StepContext{Process: "p", Path: "p/s", Roles: &methodology.Responsibilities{Responsible: "builder"}}
	got := sc.section()
	if !strings.Contains(got, `role "builder"`) || !strings.Contains(got, "start this step fresh") {
		t.Fatalf("section: %s", got)
	}
}

// The calls of the planner are the first calls of the step they choose (the step takes them over, newStep).
func TestPlannerCallsAreNumberedInTheirStep(t *testing.T) {
	var metas []llm.CallMeta
	rec := &planRecorder{client: llm.ClientFunc(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		metas = append(metas, llm.MetaFrom(ctx))
		return llm.Response{Text: "{}"}, nil
	})}
	ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceEngine, ProcessID: "p", ChangeID: "c", Step: 3, Agent: "ag"})
	for range 2 {
		if _, err := rec.complete(ctx, llm.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(metas) != 2 || metas[0].Step != 3 || metas[0].Call != 0 || metas[1].Call != 1 || metas[1].Source != llm.SourceEngine || metas[1].Agent != "ag" {
		t.Fatalf("%+v", metas)
	}
}

func TestRelaunchedTitlesDoNotPileUp(t *testing.T) {
	if got := baseTitle("design it (relaunch from step 4) (relaunch from step 2)"); got != "design it" {
		t.Fatalf("baseTitle = %q", got)
	}
}
