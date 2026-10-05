package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// The decision loop (ADR 0009 §4): a point on two options, ruled undecidable with a question, blocked until the
// question is answered, ruled by an agent below the threshold, the ruling ratified by a person: the option is
// selected (its version joins the change branch), the other one rejected.
func TestDecisionLoop(t *testing.T) { forEachRepo(t, testDecisionLoop) }

func testDecisionLoop(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	write := func(option, title string) domain.NodeRef {
		t.Helper()
		added := must[[]domain.ChangeImpact](t)(g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: title, Flow: option}}))
		cn := must[domain.ChangeImpact](t)(g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Flow: option, Properties: map[string]any{"title": title}}))
		must[domain.ChangeImpact](t)(g.ReviewNodeOn(ctx, c.ID, option, "", cn.ID, domain.ReviewAccepted, "u", "ok"))
		return *cn.Post
	}
	a := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe"}))
	b := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "adyen"}))
	va := write(a.ID, "Use Stripe")
	write(b.ID, "Use Adyen")

	d := must[domain.DecisionPoint](t)(g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "Which PSP?", Criteria: []string{"fees", "markets"}, By: "u"}))
	if d.Status != domain.PointOpen || len(d.Options) != 2 || d.Threshold != DefaultDecisionThreshold || d.MaxRounds != DefaultDecisionRounds {
		t.Fatalf("opened: %+v", d)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with a pending decision: %v", err)
	}
	if _, err := g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Option: "other", Confidence: 1, Justification: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an option the point does not choose among: %v", err)
	}
	// undecidable: the fees are unknown
	d = must[domain.DecisionPoint](t)(g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeUndecidable,
		Justification: "the fees decide", Questions: []string{"What are the fees of Adyen?"}, By: "agent"}))
	if d.Status != domain.PointBlocked || d.Rounds != 1 || len(d.Questions) != 1 {
		t.Fatalf("blocked: %+v", d)
	}
	if _, err := g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Option: a.ID, Confidence: 1, Justification: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a blocked point is not ruled: %v", err)
	}
	bb := must[domain.Blackboard](t)(g.Blackboard(ctx, c.ID))
	points, options := domain.DecisionPointsOf(bb), domain.OptionsOf(bb)
	if len(points) != 1 || len(options) != 2 || points[0].OpenQuestions() != 1 {
		t.Fatalf("blackboard: %+v %+v", points, options)
	}
	d = must[domain.DecisionPoint](t)(g.AnswerQuestion(ctx, c.ID, d.Questions[0].ID, "2.9% per transaction", "", "analyst"))
	if d.Status != domain.PointOpen || d.Questions[0].Answer == "" {
		t.Fatalf("answered: %+v", d)
	}
	// an agent below the threshold: a person ratifies
	d = must[domain.DecisionPoint](t)(g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Option: a.ID, Confidence: 0.5,
		Justification: "cheaper in our markets", By: "agent"}))
	if d.Status != domain.PointRatifying {
		t.Fatalf("ratifying: %+v", d)
	}
	if _, err := g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Option: b.ID, Confidence: 0.9, Justification: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an agent does not rule over a pending ratification: %v", err)
	}
	d = must[domain.DecisionPoint](t)(g.RatifyDecision(ctx, c.ID, d.ID, true, "ann", "agreed"))
	if d.Status != domain.PointDecided || d.Option != a.ID || d.DecidedBy != "ann" {
		t.Fatalf("decided: %+v", d)
	}
	opts := must[[]domain.Flow](t)(g.Options(ctx, c.ID))
	if opts[0].OptionStatus() != domain.OptionSelected || opts[1].OptionStatus() != domain.OptionRejected {
		t.Fatalf("options after the decision: %+v", opts)
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if !res.Contains(va) {
		t.Fatalf("the chosen option lands: %v", res.Nodes)
	}
}

// Safeguards: a point that keeps failing to settle, or past its deadline, is escalated: only a person rules it.
func TestDecisionEscalation(t *testing.T) { forEachRepo(t, testDecisionEscalation) }

func testDecisionEscalation(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "x", BaselineID: f.base.ID}))
	d := must[domain.DecisionPoint](t)(g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "Go?", Options: []string{}, MaxRounds: 1, MaxDuration: time.Hour}))
	d = must[domain.DecisionPoint](t)(g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeUndecidable, Justification: "unknown", Questions: []string{"cost?"}}))
	must[domain.DecisionPoint](t)(g.AnswerQuestion(ctx, c.ID, d.Questions[0].ID, "cheap", "", "u"))
	d = must[[]domain.DecisionPoint](t)(g.DecisionPoints(ctx, c.ID))[0]
	if d.Status != domain.PointEscalated || !d.NeedsHuman() {
		t.Fatalf("one round: escalated: %+v", d)
	}
	if _, err := g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Confidence: 1, Justification: "go"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an agent rules an escalated point: %v", err)
	}
	d = must[domain.DecisionPoint](t)(g.RuleDecision(ctx, c.ID, RuleRequest{Point: d.ID, Outcome: domain.OutcomeDecided, Confidence: 1, Justification: "go", Human: true, By: "ann"}))
	if d.Status != domain.PointDecided {
		t.Fatalf("a person decides: %+v", d)
	}
	// the deadline
	e := must[domain.DecisionPoint](t)(g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "When?", Options: []string{}, MaxDuration: time.Hour}))
	now = now.Add(2 * time.Hour)
	if e = must[[]domain.DecisionPoint](t)(g.DecisionPoints(ctx, c.ID))[1]; e.Status != domain.PointEscalated {
		t.Fatalf("past the deadline: %+v", e)
	}
}

// Selecting an option by hand settles the decision points that chose among the options.
func TestSelectOptionSettlesItsDecision(t *testing.T) {
	forEachRepo(t, testSelectOptionSettlesItsDecision)
}

func testSelectOptionSettlesItsDecision(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "x", BaselineID: f.base.ID, OwnBranch: true}))
	a := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "a"}))
	must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "b"}))
	must[domain.DecisionPoint](t)(g.OpenDecision(ctx, c.ID, OpenDecisionRequest{Question: "a or b?"}))
	must[domain.Flow](t)(g.SelectOption(ctx, c.ID, a.ID, "ann"))
	d := must[[]domain.DecisionPoint](t)(g.DecisionPoints(ctx, c.ID))[0]
	if d.Status != domain.PointDecided || d.Option != a.ID || d.DecidedBy != "ann" {
		t.Fatalf("settled: %+v", d)
	}
}
