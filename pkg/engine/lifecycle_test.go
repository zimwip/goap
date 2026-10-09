package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine/blackboard"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

// staticLifecycle is the lifecycle every methodology of a test names.
type staticLifecycle struct{ lc domain.Lifecycle }

func (s staticLifecycle) Lifecycle(context.Context, string) (*domain.Lifecycle, error) {
	return &s.lc, nil
}

// lifecycleWorld is a graph whose changes are guarded by the engine (ADR 0098), the engine following one lifecycle and
// one methodology whose conditions read the data of the change (the test sets the world through it).
type lifecycleWorld struct {
	g   *graph.Graph
	e   *Engine
	c   domain.Change
	ctx context.Context
}

func newLifecycleWorld(t *testing.T, lc domain.Lifecycle) *lifecycleWorld {
	t.Helper()
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	g.DecisionPolicy = decision.Policy{}
	m := methodology.Methodology{Name: "m", Version: "1", Namespace: domain.DefaultNamespace,
		Conditions: []methodology.Condition{
			{Name: "analysed", Expr: `has(change.data.analysed) && change.data.analysed == true`},
			{Name: "blocked", Expr: `has(change.data.blocked) && change.data.blocked == true`},
			{Name: "docs", Expr: `has(change.data.docs) && change.data.docs == true`},
		},
		Goals: []methodology.Goal{{Name: "g", Pre: map[string]bool{"analysed": true}}}}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Graph: g, Methodologies: StaticMethodologies{"m": cm}, Lifecycles: staticLifecycle{lc}}
	g.Guardians = map[string]graph.Guardian{GuardianName: Guardian{Engine: e}}
	g.DefaultGuardian = GuardianName
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "lifecycle", Methodology: "m", OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Guardian != GuardianName {
		t.Fatalf("the change is guarded by the engine: %q", c.Guardian)
	}
	return &lifecycleWorld{g: g, e: e, c: c, ctx: ctx}
}

func (w *lifecycleWorld) move(name, decision string) error {
	_, err := w.e.TransitionChange(w.ctx, w.c.ID, TransitionRequest{Transition: name, Decision: decision, By: "u"})
	return err
}

func (w *lifecycleWorld) set(t *testing.T, data map[string]any) {
	t.Helper()
	if _, err := w.g.UpdateChange(w.ctx, w.c.ID, graph.ChangePatch{Data: data}); err != nil {
		t.Fatal(err)
	}
}

func (w *lifecycleWorld) view(t *testing.T) blackboard.View {
	t.Helper()
	objs, err := w.g.Objects(w.ctx, w.c.ID, domain.ObjectFilter{Types: []string{blackboard.TypeState, blackboard.TypeTransition}})
	if err != nil {
		t.Fatal(err)
	}
	return blackboard.View{Objects: objs}
}

func (w *lifecycleWorld) create(t *testing.T, key string) domain.ChangeImpactID {
	t.Helper()
	cn, err := w.g.ImpactNodeCreate(w.ctx, w.c.ID, graph.NodeCreate{Key: key, Type: "Note", Properties: map[string]any{"title": key}, Rationale: "new"})
	if err != nil {
		t.Fatal(err)
	}
	return cn.ID
}

func (w *lifecycleWorld) edit(id domain.ChangeImpactID, title string) error {
	_, err := w.g.ImpactNodeUpdate(w.ctx, w.c.ID, id, graph.NodeUpdate{Properties: map[string]any{"title": title}})
	return err
}

func (w *lifecycleWorld) acceptAll(t *testing.T) {
	t.Helper()
	ns, err := w.g.ListChangeImpacts(w.ctx, w.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.Review == domain.ReviewAccepted {
			continue
		}
		if _, err := w.g.ImpactNodeReviewOn(w.ctx, w.c.ID, "", "", n.ID, domain.ReviewAccepted, "reviewer", "ok"); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *lifecycleWorld) decide(t *testing.T, option string) string {
	t.Helper()
	p, err := w.g.OpenDecision(w.ctx, w.c.ID, graph.OpenDecisionRequest{Question: "gate?", Policy: map[string]any{decision.KeyDecider: decision.DeciderHuman}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.RuleDecision(w.ctx, w.c.ID, graph.RuleRequest{Point: p.ID, Outcome: domain.OutcomeDecided, Option: option, Confidence: 1, Justification: "ok", Human: true, By: "u"}); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

const (
	gateGo     = `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && world["analysed"]`
	gateRework = `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "rework")`
)

// The lifecycle of a change is its methodology's, run by the engine (ADR 0058, 0098): the change starts in the initial
// state and knows none; a move is checked (the transition leaves the state, its gate holds, a decision point gates one
// move only) and recorded as execution@Transition and execution@State; the impacts started in a state the change has
// left are frozen by its guardian, going back unfreezes them and reopens the reviews of what was accepted after; the
// change lands in a final state only.
func TestChangeLifecycle(t *testing.T) {
	w := newLifecycleWorld(t, domain.Lifecycle{Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "analysing"}, {Name: "implementing"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{
			{Name: "analyse", From: "proposed", To: "analysing"},
			{Name: "implement", From: "analysing", To: "implementing", Guard: gateGo},
			{Name: "rework", From: "implementing", To: "analysing", Guard: gateRework},
			{Name: "finish", From: "implementing", To: "done"},
		}})
	if _, ok := w.view(t).State(); ok {
		t.Fatal("nothing is recorded before the first move: the change is in the initial state")
	}
	if err := w.move("finish", ""); !errors.Is(err, changeapi.ErrInvalid) {
		t.Fatalf("no such transition out of proposed: %v", err)
	}
	if err := w.move("analyse", ""); err != nil {
		t.Fatal(err)
	}
	analysis := w.create(t, "N-1")

	first := w.decide(t, "go")
	if err := w.move("implement", ""); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a gate needs its decision: %v", err)
	}
	if err := w.move("implement", first); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a gate needs the expected world state: %v", err)
	}
	w.set(t, map[string]any{"analysed": true})
	if err := w.move("implement", first); err != nil {
		t.Fatal(err)
	}
	v := w.view(t)
	if st, _ := v.State(); st.State != "implementing" || st.Lifecycle != "maturity" || len(v.Moves()) != 2 || v.Moves()[1].Decision != first || v.Moves()[1].By != "u" {
		t.Fatalf("state %+v, moves %+v", st, v.Moves())
	}

	// the analysis is frozen, what is new is not
	if err := w.edit(analysis, "late"); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("an impact of a state the change left is frozen: %v", err)
	}
	impl := w.create(t, "N-2")
	if err := w.edit(impl, "better"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.CommitChange(w.ctx, w.c.ID, ""); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a change is committed in a final state only: %v", err)
	}
	w.acceptAll(t)

	// going back needs a new decision: a point gates one transition only
	if err := w.move("rework", first); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a decision point is consumed by the transition it gated: %v", err)
	}
	if err := w.move("rework", w.decide(t, "go")); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("the decision must say rework: %v", err)
	}
	if err := w.move("rework", w.decide(t, "rework")); err != nil {
		t.Fatal(err)
	}
	if err := w.edit(analysis, "revised"); err != nil {
		t.Fatalf("going back unfreezes the analysis: %v", err)
	}
	if err := w.edit(impl, "stale"); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("the implementation is frozen in turn: %v", err)
	}
	ns, err := w.g.ListChangeImpacts(w.ctx, w.c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		if n.ID == impl && n.Review != domain.ReviewProposed {
			t.Fatalf("what was accepted after the state was left is reviewed again: %s", n.Review)
		}
	}

	// the lifecycle ends in a final state, then the change applies
	if err := w.move("implement", w.decide(t, "go")); err != nil {
		t.Fatal(err)
	}
	if err := w.move("finish", ""); err != nil {
		t.Fatal(err)
	}
	w.acceptAll(t)
	if _, err := w.g.Apply(w.ctx, w.c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.move("rework", ""); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("an applied change does not move: %v", err)
	}
}

// Nobody but the engine writes the state of a change: the guardian's records are concurrent-safe (a move over a state
// read before another move is a conflict).
func TestChangeStateWrittenOverTheStateRead(t *testing.T) {
	w := newLifecycleWorld(t, domain.Lifecycle{Name: "l", Initial: "a", States: []domain.LifecycleState{{Name: "a"}, {Name: "b", Final: true}},
		Transitions: []domain.Transition{{Name: "go", From: "a", To: "b"}}})
	if _, err := w.g.PutObjects(w.ctx, w.c.ID, []domain.ObjectWrite{blackboard.SetState("l", "a", 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.PutObjects(w.ctx, w.c.ID, []domain.ObjectWrite{blackboard.SetState("l", "b", 0)}); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a write over another version: %v", err)
	}
	if err := w.move("go", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.view(t).State(); st.State != "b" || st.Version != 2 {
		t.Fatalf("state %+v", st)
	}
}

// A gate splits its criteria in vetos (one unmet blocks) and objectives (an unmet one blocks too unless a derogation in
// force names it, then the transition goes with reserve and the move says so, ADR 0075 §3).
func TestGateVetosAndObjectives(t *testing.T) {
	risk.Register()
	w := newLifecycleWorld(t, domain.Lifecycle{Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{{Name: "ship", From: "proposed", To: "done",
			Vetos:      []domain.Criterion{{Name: "no_blocker", Expr: `!world["blocked"]`}},
			Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}}}}})
	w.set(t, map[string]any{"blocked": true})
	if err := w.move("ship", ""); !errors.Is(err, changeapi.ErrConflict) || !strings.Contains(err.Error(), "no_blocker") {
		t.Fatalf("a veto not met blocks: %v", err)
	}
	w.set(t, map[string]any{"blocked": false})
	if err := w.move("ship", ""); !errors.Is(err, changeapi.ErrConflict) || !strings.Contains(err.Error(), "docs") {
		t.Fatalf("an objective not met blocks: %v", err)
	}
	drg := func(rule string) domain.ChangeItem {
		return domain.ChangeItem{Kind: risk.KindDerogation, Data: map[string]any{"key": "DRG-1", "rule": rule, "target": string(w.c.ID), "reason": "deadline", "signatory": "alice",
			"expires": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	}
	if _, err := w.g.AddItems(w.ctx, w.c.ID, []domain.ChangeItem{drg("something else")}); err != nil {
		t.Fatal(err)
	}
	if err := w.move("ship", ""); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a derogation of another rule covers nothing: %v", err)
	}
	// a veto is never compensated
	w.set(t, map[string]any{"blocked": true})
	if _, err := w.g.AddItems(w.ctx, w.c.ID, []domain.ChangeItem{drg("docs")}); err != nil {
		t.Fatal(err)
	}
	if err := w.move("ship", ""); !errors.Is(err, changeapi.ErrConflict) {
		t.Fatalf("a derogation does not waive a veto: %v", err)
	}
	w.set(t, map[string]any{"blocked": false})
	if err := w.move("ship", ""); err != nil {
		t.Fatalf("covered objective: %v", err)
	}
	v := w.view(t)
	st, _ := v.State()
	objs, _ := w.g.Objects(w.ctx, w.c.ID, domain.ObjectFilter{Types: []string{blackboard.TypeTransition}})
	if st.State != "done" || len(objs) != 1 {
		t.Fatalf("state %q, moves %d", st.State, len(objs))
	}
	if r := fmt.Sprint(objs[0].Value["reserve"]); r != "map[docs:DRG-1]" || fmt.Sprint(objs[0].Value["unmet"]) != "[docs]" {
		t.Fatalf("the move says what it goes with reserve of: %v", objs[0].Value)
	}
}
