package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
)

// A gate's go freezes what it names beyond the impacts of the state it leaves (ADR 0132 §4): fields of the change,
// node types; going back lifts it. A collegial decision is ruled in the seats of its board.
func TestGateFreezesAndSeats(t *testing.T) { forEachRepo(t, testGateFreezesAndSeats) }

func testGateFreezesAndSeats(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	lcs := &lifecycles{world: map[string]bool{}, lc: domain.Lifecycle{Name: "maturity", Initial: "framing",
		States: []domain.LifecycleState{{Name: "framing"}, {Name: "analysing"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{
			{Name: "frame", From: "framing", To: "analysing", Guard: `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go")`,
				Freezes: &domain.Freezes{Fields: []string{domain.FreezeIntent}, Types: []string{"Spec"}, KeepPhase: true}},
			{Name: "reframe", From: "analysing", To: "framing"},
		}}}
	w.g.Lifecycles = lcs
	w.g.DecisionPolicy = decision.Policy{}
	seats := map[string]string{"alice": "lead", "bob": "analyst"}
	w.g.SeatAuthorizer = func(ctx context.Context, _ domain.Change, role string) error {
		if seats[authz.From(ctx).Subject] != role {
			return authz.ErrForbidden
		}
		return nil
	}
	g := w.g
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "gated", Intent: "first", Methodology: "m", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "N-1", Type: "Note", Rationale: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	note := ns[0].ID
	if err := w.write(c, note, edit{Properties: map[string]any{"title": "n"}}); err != nil {
		t.Fatal(err)
	}

	// the board: two seats, unanimity
	p, err := g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "go?", Options: []string{}, Choices: []string{"go", "no_go"},
		Policy: map[string]any{decision.KeyBoard: []any{"lead", "analyst"}, decision.KeyChair: "lead"}})
	if err != nil {
		t.Fatal(err)
	}
	as := func(who string) context.Context { return authz.With(ctx, authz.Principal{Subject: who}) }
	rule := func(who, role string) (domain.DecisionPoint, error) {
		return g.RuleDecision(as(who), c.ID, RuleRequest{Point: p.ID, Outcome: domain.OutcomeDecided, Option: "go", Confidence: 1,
			Justification: "ready", Human: true, By: who, Role: role})
	}
	if _, err := rule("alice", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a board is ruled in a seat: %v", err)
	}
	if _, err := rule("alice", "analyst"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a seat the person does not hold: %v", err)
	}
	if d, err := rule("alice", "lead"); err != nil || d.Status == domain.PointDecided {
		t.Fatalf("one seat of two: %v %+v", err, d)
	}
	if d, err := rule("bob", "analyst"); err != nil || d.Status != domain.PointDecided || d.Option != "go" {
		t.Fatalf("both seats said go: %v %+v", err, d)
	}
	if _, err := g.TransitionChange(ctx, c.ID, TransitionRequest{Transition: "frame", Decision: p.ID, By: "alice"}); err != nil {
		t.Fatal(err)
	}

	// frozen: the intent, the creation of a spec; kept: the impacts of framing (keepPhase)
	intent := "second"
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Intent: &intent}); !errors.Is(err, ErrConflict) {
		t.Fatalf("the intent is frozen: %v", err)
	}
	title := "renamed"
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title}); err != nil {
		t.Fatalf("the title is not: %v", err)
	}
	if _, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "SPEC-9", Type: "Spec", Rationale: "late"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("specs are frozen: %v", err)
	}
	if err := w.write(c, note, edit{Properties: map[string]any{"title": "kept"}}); err != nil {
		t.Fatalf("the impacts of framing stay open: %v", err)
	}

	// going back lifts the freezes
	if _, err := g.TransitionChange(ctx, c.ID, TransitionRequest{Transition: "reframe", By: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Intent: &intent}); err != nil {
		t.Fatalf("back in framing, the intent is free again: %v", err)
	}
}
