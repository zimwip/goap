package graph

import (
	"context"
	"errors"
	"fmt"
	"github.com/zimwip/goap/contracts/risk"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/guard"
)

// lifecycles is a ChangeLifecycles whose world state the test sets.
type lifecycles struct {
	lc    domain.Lifecycle
	world map[string]bool
	goal  string
}

func (l *lifecycles) MethodologyVersion(context.Context, string) (string, error)   { return "", nil }
func (l *lifecycles) Lifecycle(context.Context, string) (*domain.Lifecycle, error) { return &l.lc, nil }
func (l *lifecycles) DefaultGoal(context.Context, string) (string, error)          { return l.goal, nil }

// Gate stands for the guardian of the methodology (the registry's condition.CheckGate, which the graph does not
// import, ADR 0125): the vetos and objectives over the world and the decision points, an unmet objective covered by
// a derogation in force naming it.
func (l *lifecycles) Gate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error) {
	var res domain.GateResult
	for _, c := range t.Vetos {
		ok, err := l.Guard(ctx, bb, c.Expr, t.Name, decision)
		if err != nil {
			return res, err
		}
		if !ok {
			res.Vetoed = append(res.Vetoed, c.Name)
		}
	}
	for _, c := range t.Objectives {
		ok, err := l.Guard(ctx, bb, c.Expr, t.Name, decision)
		if err != nil {
			return res, err
		}
		if ok {
			continue
		}
		res.Unmet = append(res.Unmet, c.Name)
		for _, d := range risk.Derogations(bb.Change) {
			if d.Rule == c.Name && d.InForce(time.Now()) {
				res.Reserve = map[string]string{c.Name: d.Key}
				break
			}
		}
	}
	return res, nil
}

// Guard evaluates expr over the world, the change (with the transition and the decision) and its decision points.
func (l *lifecycles) Guard(_ context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error) {
	gd, err := guard.Compile(expr)
	if err != nil {
		return false, err
	}
	var f guard.Facts
	for _, d := range domain.DecisionPointsOf(bb) {
		f.DecisionPoints = append(f.DecisionPoints, map[string]any{"id": d.ID, "status": d.Status, "option": d.Option})
	}
	return gd.CheckChange(map[string]any{"id": string(bb.Change.ID), "transition": transition, "decision": decision}, f, l.world)
}

func (l *lifecycles) Commit(context.Context, domain.Change, domain.Blackboard) (bool, bool, error) {
	return false, false, nil
}

func (l *lifecycles) Guardians(context.Context, string, string) ([]domain.GuardianRef, error) {
	return nil, nil
}
func (l *lifecycles) Integrates(context.Context, string, string) (bool, error) { return false, nil }
func (l *lifecycles) CheckGuardians(context.Context, domain.Blackboard, string, []domain.Change) (string, error) {
	return "", nil
}

// commitGate is a guardian that has only its say on the merge of a change (the goal of an activity, ADR 0066).
type commitGate func(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error)

func (commitGate) MethodologyVersion(context.Context, string) (string, error)   { return "", nil }
func (commitGate) Lifecycle(context.Context, string) (*domain.Lifecycle, error) { return nil, nil }
func (commitGate) DefaultGoal(context.Context, string) (string, error)          { return "", nil }
func (commitGate) Guard(context.Context, domain.Blackboard, string, string, string) (bool, error) {
	return true, nil
}
func (commitGate) Gate(context.Context, domain.Blackboard, domain.Transition, string) (domain.GateResult, error) {
	return domain.GateResult{}, nil
}
func (commitGate) Guardians(context.Context, string, string) ([]domain.GuardianRef, error) {
	return nil, nil
}
func (commitGate) Integrates(context.Context, string, string) (bool, error) { return false, nil }
func (commitGate) CheckGuardians(context.Context, domain.Blackboard, string, []domain.Change) (string, error) {
	return "", nil
}
func (f commitGate) Commit(ctx context.Context, c domain.Change, bb domain.Blackboard) (bool, bool, error) {
	return f(ctx, c, bb)
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
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: domain.TypeTransition, Data: map[string]any{"to": "done"}}}); !errors.Is(err, ErrInvalid) {
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
	// the transitions out of the state, judged without being taken
	judged := func() TransitionCheck {
		t.Helper()
		state, ts, err := g.ChangeTransitions(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state != "analysing" || len(ts) != 1 || ts[0].Name != "implement" {
			t.Fatalf("the transitions out of analysing: %q %+v", state, ts)
		}
		return ts[0]
	}
	if j := judged(); j.Allowed || len(j.Blocks) != 1 || j.Blocks[0].Kind != BlockDecision {
		t.Fatalf("a guard reading a decision with none decided: %+v", j)
	}
	first := decide("go")
	if j := judged(); j.Allowed || len(j.Blocks) != 1 || j.Blocks[0].Kind != BlockGuard || !strings.Contains(j.Blocks[0].Reason, "analysed") {
		t.Fatalf("a guard the world does not satisfy: %+v", j)
	}
	g.ChangeAuthorizer = func(context.Context, domain.Change, domain.Transition) error { return authz.ErrForbidden }
	if j := judged(); j.Allowed || len(j.Blocks) != 2 || j.Blocks[0].Kind != BlockPermission || !strings.Contains(j.Blocks[0].Reason, "change:transition on project PROJ-ROOT") {
		t.Fatalf("every check that refuses is listed, the permission first: %+v", j)
	}
	g.ChangeAuthorizer = nil
	lcs.world["analysed"] = true
	if j := judged(); !j.Allowed || len(j.Decisions) != 1 || j.Decisions[0] != first {
		t.Fatalf("allowed with the decision it passes with: %+v", j)
	}
	lcs.world["analysed"] = false
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
	w.accept(t, c)
	// the merge guards hold a change that is not in a final state (ADR 0102: FinalStateGuard)
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		cur, err := tx.Change(ctx, c.ID)
		if err != nil {
			return err
		}
		return g.checkMerge(ctx, tx, cur, true)
	}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "final state") {
		t.Fatalf("a change is merged in a final state only: %v", err)
	}

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
	if _, err := g.apply(ctx, c.ID, ""); err != nil {
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
	judged := func() TransitionCheck {
		t.Helper()
		_, ts, err := g.ChangeTransitions(ctx, c.ID)
		if err != nil || len(ts) != 1 {
			t.Fatal(ts, err)
		}
		return ts[0]
	}
	if j := judged(); j.Allowed || j.Blocks[0].Kind != BlockVeto || j.Blocks[0].Reason != "vetoed by no_blocker" {
		t.Fatalf("judged vetoed: %+v", j)
	}
	lcs.world["blocked"] = false
	if j := judged(); j.Allowed || j.Blocks[0].Kind != BlockObjective || !strings.Contains(j.Blocks[0].Reason, "docs") {
		t.Fatalf("judged short of an objective: %+v", j)
	}
	if err := ship(); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "docs") {
		t.Fatalf("an objective not met blocks: %v", err)
	}
	drg := func(rule string) domain.ChangeItem {
		return domain.ChangeItem{Type: risk.TypeDerogation, Data: map[string]any{"key": "DRG-1", "rule": rule, "target": string(c.ID), "reason": "deadline", "signatory": "alice",
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
	if j := judged(); !j.Allowed || fmt.Sprint(j.Reserve) != "[docs]" {
		t.Fatalf("judged allowed with reserve: %+v", j)
	}
	if err := ship(); err != nil {
		t.Fatalf("covered objective: %v", err)
	}
	got, _ := g.Change(ctx, c.ID)
	moves := got.ItemsOfType(domain.TypeTransition)
	if got.State != "done" || len(moves) != 1 {
		t.Fatalf("state %q, moves %d", got.State, len(moves))
	}
	if r := fmt.Sprint(moves[0].Data["reserve"]); r != "map[docs:DRG-1]" || fmt.Sprint(moves[0].Data["unmet"]) != "[docs]" {
		t.Fatalf("the move says what it goes with reserve of: %v", moves[0].Data)
	}
}

// A change starts with the main goal of its methodology, unless it names one; a sub-change works towards its parent's
// goal (ADR 0096). The creation default is not a header edit: nothing is logged.
func TestChangeDefaultGoal(t *testing.T) { forEachRepo(t, testChangeDefaultGoal) }

func testChangeDefaultGoal(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	lcs := &lifecycles{goal: "deliver"}
	new := func(in NewChange) domain.Change {
		t.Helper()
		in.ProjectID, in.BaselineID = "PROJ-ROOT", w.base.ID
		c, err := g.CreateChange(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// no registry: no default
	if c := new(NewChange{Title: "a", Methodology: "m"}); c.Goal != "" {
		t.Fatalf("without a registry: %q", c.Goal)
	}
	g.Lifecycles = lcs
	lcs.lc = domain.Lifecycle{}
	c := new(NewChange{Title: "b", Methodology: "m", OwnBranch: true, OwnerOrg: "ORG-DIGITAL"})
	if c.Goal != "deliver" {
		t.Fatalf("default goal: %q", c.Goal)
	}
	if got, _ := g.Change(ctx, c.ID); got.Goal != "deliver" {
		t.Fatalf("stored goal: %q", got.Goal)
	}
	if c := new(NewChange{Title: "c", Methodology: "m", Goal: "other"}); c.Goal != "other" {
		t.Fatalf("override: %q", c.Goal)
	}
	if c := new(NewChange{Title: "d"}); c.Goal != "" {
		t.Fatalf("no methodology: %q", c.Goal)
	}
	sub, err := g.CreateChange(ctx, NewChange{Title: "s", ParentID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Goal != "deliver" {
		t.Fatalf("sub-change goal: %q", sub.Goal)
	}
	g2 := "edited"
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Goal: &g2}); err != nil {
		t.Fatal(err)
	}
	sub2, err := g.CreateChange(ctx, NewChange{Title: "s2", ParentID: c.ID})
	if err != nil || sub2.Goal != "edited" {
		t.Fatalf("sub-change of an edited parent: %q %v", sub2.Goal, err)
	}
	sub3, err := g.CreateChange(ctx, NewChange{Title: "s3", ParentID: c.ID, Goal: "own"})
	if err != nil || sub3.Goal != "own" {
		t.Fatalf("sub-change override: %q %v", sub3.Goal, err)
	}
}
