package graph

import (
	"context"
	"errors"
	"fmt"
	"github.com/zimwip/goap/pkg/risk"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
)

// lifecycles is a ChangeLifecycles whose world state the test sets.
type lifecycles struct {
	lc    domain.Lifecycle
	world map[string]bool
}

func (l *lifecycles) Lifecycle(context.Context, string) (*domain.Lifecycle, error) { return &l.lc, nil }
func (l *lifecycles) Gate(_ context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error) {
	return condition.CheckGate(t, bb, l.world, decision)
}

func (l *lifecycles) Guard(_ context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error) {
	return condition.CheckGuard(expr, bb, l.world, transition, decision)
}

const (
	gateGo     = `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && world["analysed"]`
	gateRework = `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "rework")`
)

func TestChangeLifecycle(t *testing.T) { forEachRepo(t, testChangeLifecycle) }

func testChangeLifecycle(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	lcs := &lifecycles{world: map[string]bool{"analysed": false}, lc: domain.Lifecycle{Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "analysing"}, {Name: "implementing"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{
			{Name: "analyse", From: "proposed", To: "analysing"},
			{Name: "implement", From: "analysing", To: "implementing", Guard: gateGo},
			{Name: "rework", From: "implementing", To: "analysing", Guard: gateRework},
			{Name: "finish", From: "implementing", To: "done"},
		}}}
	w.g.Lifecycles = lcs
	w.g.DecisionPolicy = decision.Policy{}
	g := w.g

	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "lifecycle", Methodology: "m", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if c.Lifecycle != "maturity" || c.State != "proposed" {
		t.Fatalf("a change starts in the initial state: %q %q", c.Lifecycle, c.State)
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindTransition, Data: map[string]any{"to": "done"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a transition is written by TransitionChange only: %v", err)
	}
	move := func(name, decision string) error {
		_, err := g.TransitionChange(ctx, c.ID, TransitionRequest{Transition: name, Decision: decision, By: "u"})
		return err
	}
	if err := move("finish", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no such transition out of proposed: %v", err)
	}
	if err := move("analyse", ""); err != nil {
		t.Fatal(err)
	}
	create := func(key string) domain.ChangeImpactID {
		ns, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: key, Type: "Note", Rationale: "new"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.write(c, ns[0].ID, edit{Properties: map[string]any{"title": key}}); err != nil {
			t.Fatal(err)
		}
		return ns[0].ID
	}
	analysis := create("N-1")

	decide := func(option string) string {
		p, err := g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "gate?", Policy: map[string]any{decision.KeyDecider: decision.DeciderHuman}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.RuleDecision(ctx, c.ID, RuleRequest{Point: p.ID, Outcome: domain.OutcomeDecided, Option: option, Confidence: 1, Justification: "ok", Human: true, By: "u"}); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	first := decide("go")
	if err := move("implement", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a gate needs its decision: %v", err)
	}
	if err := move("implement", first); !errors.Is(err, ErrConflict) {
		t.Fatalf("a gate needs the expected world state: %v", err)
	}
	lcs.world["analysed"] = true
	if err := move("implement", first); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.Change(ctx, c.ID); got.State != "implementing" || len(got.StateMoves()) != 2 {
		t.Fatalf("state %q, moves %+v", got.State, got.StateMoves())
	}

	// the analysis is frozen, what is new is not
	if err := w.write(c, analysis, edit{Properties: map[string]any{"title": "late"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an impact of a state the change left is frozen: %v", err)
	}
	impl := create("N-2")
	if _, err := g.CommitChange(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change is committed in a final state only: %v", err)
	}
	w.accept(t, c)

	// going back needs a new decision: a point gates one transition only
	if err := move("rework", first); !errors.Is(err, ErrConflict) {
		t.Fatalf("a decision point is consumed by the transition it gated: %v", err)
	}
	if err := move("rework", decide("go")); !errors.Is(err, ErrConflict) {
		t.Fatalf("the decision must say rework: %v", err)
	}
	if err := move("rework", decide("rework")); err != nil {
		t.Fatal(err)
	}
	if err := w.write(c, analysis, edit{Properties: map[string]any{"title": "revised"}}); err != nil {
		t.Fatalf("going back unfreezes the analysis: %v", err)
	}
	if err := w.write(c, impl, edit{Properties: map[string]any{"title": "stale"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the implementation is frozen in turn: %v", err)
	}
	ns, err := g.ListChangeImpacts(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.ID == impl && n.Review != domain.ReviewProposed {
			t.Fatalf("what was written after the phase must be reviewed again: %s", n.Review)
		}
		if n.ID == analysis && n.Review != domain.ReviewProposed {
			t.Fatalf("the analysis checked out again is reviewed again (ADR 0076): %s", n.Review)
		}
	}

	// the lifecycle ends in a final state, then the change applies
	if err := move("implement", decide("go")); err != nil {
		t.Fatal(err)
	}
	if err := move("finish", ""); err != nil {
		t.Fatal(err)
	}
	w.accept(t, c)
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

// A gate splits its criteria in vetos (one unmet blocks) and objectives (an unmet one blocks too unless a derogation in
// force names it, then the transition goes with reserve and says so, ADR 0075 §3).
func TestGateVetosAndObjectives(t *testing.T) { forEachRepo(t, testGateVetosAndObjectives) }

func testGateVetosAndObjectives(t *testing.T, repo Repo) {
	risk.Register()
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	lcs := &lifecycles{world: map[string]bool{"blocked": true, "docs": false}, lc: domain.Lifecycle{Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{{Name: "ship", From: "proposed", To: "done",
			Vetos:      []domain.Criterion{{Name: "no_blocker", Expr: `!world["blocked"]`}},
			Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}}}}}}
	w.g.Lifecycles = lcs
	g := w.g
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "gate", Methodology: "m", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	ship := func() error {
		_, err := g.TransitionChange(ctx, c.ID, TransitionRequest{Transition: "ship", By: "u"})
		return err
	}
	if err := ship(); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "no_blocker") {
		t.Fatalf("a veto not met blocks: %v", err)
	}
	lcs.world["blocked"] = false
	if err := ship(); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "docs") {
		t.Fatalf("an objective not met blocks: %v", err)
	}
	drg := func(rule string) domain.ChangeItem {
		return domain.ChangeItem{Kind: risk.KindDerogation, Data: map[string]any{"key": "DRG-1", "rule": rule, "target": string(c.ID), "reason": "deadline", "signatory": "alice",
			"expires": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{drg("something else")}); err != nil {
		t.Fatal(err)
	}
	if err := ship(); !errors.Is(err, ErrConflict) {
		t.Fatalf("a derogation of another rule covers nothing: %v", err)
	}
	// a veto is never compensated
	lcs.world["blocked"] = true
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{drg("docs")}); err != nil {
		t.Fatal(err)
	}
	if err := ship(); !errors.Is(err, ErrConflict) {
		t.Fatalf("a derogation does not waive a veto: %v", err)
	}
	lcs.world["blocked"] = false
	if err := ship(); err != nil {
		t.Fatalf("covered objective: %v", err)
	}
	got, _ := g.Change(ctx, c.ID)
	moves := got.ItemsOfKind(domain.KindTransition)
	if got.State != "done" || len(moves) != 1 {
		t.Fatalf("state %q, moves %d", got.State, len(moves))
	}
	if r := fmt.Sprint(moves[0].Data["reserve"]); r != "map[docs:DRG-1]" || fmt.Sprint(moves[0].Data["unmet"]) != "[docs]" {
		t.Fatalf("the move says what it goes with reserve of: %v", moves[0].Data)
	}
}

// Without vetos or objectives nothing is asked of the lifecycle, and a met objective needs no derogation.
func TestGateObjectiveMet(t *testing.T) {
	bb := domain.Blackboard{}
	res, err := condition.CheckGate(domain.Transition{Name: "t", Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}}}, bb, map[string]bool{"docs": true}, "")
	if err != nil || !res.Passed() || res.WithReserve() {
		t.Fatalf("%+v %v", res, err)
	}
}
