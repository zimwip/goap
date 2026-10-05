package registrysvc

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

// End-to-end (ADR 0058): a methodology names a lifecycle of its domain; its changes start in the initial state, and
// a gate of the lifecycle is a CEL guard over a decided decision point and the world state of the methodology,
// where "state:<name>" is the generated condition of a state.
func TestChangeLifecycleGate(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := graphWithDomains{NewGraphStore(g), NewMemoryStore()}
	reg := &Service{Store: store}
	g.Lifecycles = reg
	g.DecisionPolicy = decision.Policy{}

	d := def.Domain{Name: "alm", Version: "1", Schema: def.Schema{Lifecycles: []domain.Lifecycle{{
		Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed", Editable: true}, {Name: "analysing", Editable: true}, {Name: "implementing", Editable: true}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{
			{Name: "analyse", From: "proposed", To: "analysing"},
			{Name: "implement", From: "analysing", To: "implementing",
				Guard: `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && (world["analysed"] || actions.exists(a, a.for == change.decision))`},
		}}}}}
	if err := store.DomainStore.SaveDomain(ctx, DomainRecord{Domain: d, Status: StatusPublished}); err != nil {
		t.Fatal(err)
	}
	m := methodology.Methodology{
		Name: "delivery", Version: "1", Namespace: "alm", Lifecycle: "maturity",
		Conditions: []methodology.Condition{{Name: "analysed", Expr: `artifacts.exists(a, a.type == "analysis")`}},
		Processes: []methodology.Process{{Name: "deliver", Steps: []methodology.Step{
			{Name: "analyse", Pre: map[string]bool{"state:analysing": true}, Done: map[string]bool{"analysed": true}},
			{Name: "build", Pre: map[string]bool{"state:implementing": true}},
		}}},
	}
	if err := store.Save(ctx, Record{Methodology: m, Status: StatusPublished}); err != nil {
		t.Fatal(err)
	}
	c, err := reg.Methodology(ctx, "delivery")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Conditions.Has("state:implementing") {
		t.Fatal("state:<name> is a generated condition")
	}
	if build, _ := c.Action("deliver/build"); !build.Pre["state:implementing"] {
		t.Fatalf("a step is active in the state it names: %+v", build.Pre)
	}

	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "ship", Methodology: "delivery", Namespace: "alm"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Lifecycle != "maturity" || ch.State != "proposed" {
		t.Fatalf("a change starts in the initial state: %q %q", ch.Lifecycle, ch.State)
	}
	if other, err := g.CreateChange(ctx, graph.NewChange{Title: "free", Methodology: "no-such", Namespace: "alm"}); err != nil || other.Lifecycle != "" {
		t.Fatalf("a methodology without lifecycle leaves the change without state: %+v %v", other, err)
	}
	if _, err := g.TransitionChange(ctx, ch.ID, graph.TransitionRequest{Transition: "analyse"}); err != nil {
		t.Fatal(err)
	}
	pt, err := g.OpenDecision(ctx, ch.ID, graph.OpenDecisionRequest{Question: "analysis complete?", Policy: map[string]any{decision.KeyDecider: decision.DeciderHuman}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.RuleDecision(ctx, ch.ID, graph.RuleRequest{Point: pt.ID, Outcome: domain.OutcomeDecided, Option: "go", Confidence: 1, Justification: "ok", Human: true}); err != nil {
		t.Fatal(err)
	}
	implement := graph.TransitionRequest{Transition: "implement", Decision: pt.ID}
	if _, err := g.TransitionChange(ctx, ch.ID, implement); !errors.Is(err, graph.ErrConflict) {
		t.Fatalf("the expected world state is not reached: %v", err)
	}
	// the decision prevails over the world state once actions are taken to fill the gap
	if _, err := g.AddItems(ctx, ch.ID, []domain.ChangeItem{{Kind: risk.KindAction, Status: domain.ItemAccepted,
		Data: map[string]any{"key": "ACT-1", "title": "finish the analysis", "for": pt.ID}}}); err != nil {
		t.Fatal(err)
	}
	got, err := g.TransitionChange(ctx, ch.ID, implement)
	if err != nil || got.State != "implementing" {
		t.Fatalf("gate: %+v %v", got, err)
	}
}
