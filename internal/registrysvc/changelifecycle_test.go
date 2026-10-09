package registrysvc

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/engine/blackboard"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

// End-to-end (ADR 0058, 0098): a methodology names a lifecycle of its domain, resolved by the registry for the engine,
// which moves the state of its changes (the change knows none): they start in the initial state, and a gate of the
// lifecycle is a CEL guard over a decided decision point and the world state of the methodology, where
// "state:<name>" is the generated condition of a state.
func TestChangeLifecycleGate(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := graphWithDomains{NewGraphStore(g), NewMemoryStore()}
	reg := &Service{Store: store}
	g.Defaults = reg
	g.DecisionPolicy = decision.Policy{}
	eng := &engine.Engine{Graph: g, Methodologies: reg, Lifecycles: reg}
	state := func(id domain.ChangeID) string {
		objs, err := g.Objects(ctx, id, domain.ObjectFilter{Types: []string{blackboard.TypeState}})
		if err != nil {
			t.Fatal(err)
		}
		st, _ := (blackboard.View{Objects: objs}).State()
		return st.State
	}

	d := def.Domain{Name: "alm", Version: "1", Schema: def.Schema{Lifecycles: []domain.Lifecycle{{
		Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "analysing"}, {Name: "implementing"}, {Name: "done", Final: true}},
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

	ch, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "ship", Methodology: "delivery", Namespace: "alm"})
	if err != nil {
		t.Fatal(err)
	}
	if lc, err := reg.Lifecycle(ctx, ch.Methodology); err != nil || lc == nil || lc.Name != "maturity" || lc.Initial != "proposed" {
		t.Fatalf("the lifecycle of the methodology: %+v %v", lc, err)
	}
	// the change starts with the main goal of its methodology: its first process here (ADR 0096)
	if ch.Goal != "deliver" {
		t.Fatalf("default goal %q", ch.Goal)
	}
	if lc, err := reg.Lifecycle(ctx, "no-such"); err != nil || lc != nil {
		t.Fatalf("an unknown methodology names no lifecycle: %+v %v", lc, err)
	}
	if _, err := eng.TransitionChange(ctx, ch.ID, engine.TransitionRequest{Transition: "analyse"}); err != nil {
		t.Fatal(err)
	}
	pt, err := g.OpenDecision(ctx, ch.ID, graph.OpenDecisionRequest{Question: "analysis complete?", Policy: map[string]any{decision.KeyDecider: decision.DeciderHuman}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.RuleDecision(ctx, ch.ID, graph.RuleRequest{Point: pt.ID, Outcome: domain.OutcomeDecided, Option: "go", Confidence: 1, Justification: "ok", Human: true}); err != nil {
		t.Fatal(err)
	}
	implement := engine.TransitionRequest{Transition: "implement", Decision: pt.ID}
	if _, err := eng.TransitionChange(ctx, ch.ID, implement); !errors.Is(err, graph.ErrConflict) {
		t.Fatalf("the expected world state is not reached: %v", err)
	}
	// the decision prevails over the world state once actions are taken to fill the gap
	if _, err := g.AddItems(ctx, ch.ID, []domain.ChangeItem{{Kind: risk.KindAction, Status: domain.ItemAccepted,
		Data: map[string]any{"key": "ACT-1", "title": "finish the analysis", "for": pt.ID}}}); err != nil {
		t.Fatal(err)
	}
	got, err := eng.TransitionChange(ctx, ch.ID, implement)
	if err != nil || got.State.State != "implementing" || state(ch.ID) != "implementing" {
		t.Fatalf("gate: %+v %v", got, err)
	}
}
