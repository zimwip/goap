package registrysvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/guardianclient"

	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/def"
)

// End-to-end (ADR 0058): a methodology names a lifecycle of its domain; its changes start in the initial state, and
// a gate of the lifecycle is a CEL guard over a decided decision point and the world state of the methodology,
// where "state:<name>" is the generated condition of a state. The graph asks the registry in process, then over the
// guardian service (ADR 0168), with the same answers.
func TestChangeLifecycleGate(t *testing.T) {
	t.Run("in process", func(t *testing.T) { testChangeLifecycleGate(t, false) })
	t.Run("over RPC", func(t *testing.T) { testChangeLifecycleGate(t, true) })
}

func testChangeLifecycleGate(t *testing.T, remote bool) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	store := NewGraphStore(g)
	reg := &Service{Authz: authz.AllowAll{}, Store: store}
	g.Lifecycles = reg
	if remote {
		// the registry keeps what it read until the methodologies move: the graph asks it inside the transaction of a
		// change, and a memory graph serves one transaction at a time
		store.Follow()
		mux := http.NewServeMux()
		mux.Handle(registryv1connect.NewChangeGuardianServiceHandler(&GuardianHandler{Service: reg}))
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		g.Lifecycles = guardianclient.NewClient(srv.Client(), srv.URL)
	}
	g.DecisionPolicy = decision.Policy{}

	d := def.Domain{Name: "alm", Version: "1", Schema: def.Schema{Lifecycles: []domain.Lifecycle{{
		Name: "maturity", Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "analysing"}, {Name: "implementing"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{
			{Name: "analyse", From: "proposed", To: "analysing"},
			{Name: "implement", From: "analysing", To: "implementing",
				Guard: `decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && (world["analysed"] || actions.exists(a, a.for == change.decision))`},
		}}}}}
	if err := domainsOf(reg).Store.SaveDomain(ctx, def.Record{Domain: d, Status: StatusPublished}); err != nil {
		t.Fatal(err)
	}
	m := methodology.Methodology{
		Name: "delivery", Version: "1", Namespace: "alm", Lifecycle: "maturity",
		Conditions: []methodology.Condition{{Name: "analysed", Expr: `artifacts.exists(a, a.target == "analysis")`}},
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
	if ch.Lifecycle != "maturity" || ch.State != "proposed" {
		t.Fatalf("a change starts in the initial state: %q %q", ch.Lifecycle, ch.State)
	}
	// the change starts with the main goal of its methodology: its first process here (ADR 0096)
	if ch.Goal != "deliver" {
		t.Fatalf("default goal %q", ch.Goal)
	}
	if other, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "free", Methodology: "no-such", Namespace: "alm"}); err != nil || other.Lifecycle != "" {
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
	if _, err := g.AddItems(ctx, ch.ID, []domain.ChangeItem{{Type: risk.TypeAction, Status: domain.ItemAccepted,
		Data: map[string]any{"key": "ACT-1", "title": "finish the analysis", "for": pt.ID}}}); err != nil {
		t.Fatal(err)
	}
	got, err := g.TransitionChange(ctx, ch.ID, implement)
	if err != nil || got.State != "implementing" {
		t.Fatalf("gate: %+v %v", got, err)
	}
}
