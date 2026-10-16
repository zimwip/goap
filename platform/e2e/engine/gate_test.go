package enginee2e

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/platform/registrysvc"
)

const gatedYAML = `
name: gated
version: 1.0.0
namespace: alm
lifecycle: maturity
goal: delivery
roles:
  - {name: contributor}
  - {name: approver}
conditions:
  - {name: analysed, expr: 'artifacts.exists(x, x.target == "analysis")'}
  - {name: designed, expr: 'artifacts.exists(x, x.target == "design")'}
actions:
  - {name: analyse, kind: human, effects: {analysed: true}}
  - {name: design, kind: human, effects: {designed: true}}
processes:
  - name: delivery
    steps:
      - {name: analysis, action: analyse}
      - name: analysis_gate
        pre: {analysed: true}
        roles: {accountable: approver}
        gate:
          transition: accept
          checklist:
            - {name: analysed, role: contributor, veto: true}
            - {name: compliant, role: contributor, attest: true, veto: true}
          decision: {board: [contributor], rule: unanimity}
          freezes: {fields: [intent]}
      - name: designing
        pre: {"gate:delivery/analysis_gate": true}
        action: design
`

// maturity is the lifecycle of the changes of gated: draft, then accepted by its gate.
var maturity = domain.Lifecycle{Name: "maturity", Initial: "draft",
	States:      []domain.LifecycleState{{Name: "draft"}, {Name: "accepted"}},
	Transitions: []domain.Transition{{Name: "accept", From: "draft", To: "accepted"}}}

// gatedLifecycles is the registry's part of ChangeLifecycles for a gated methodology: its lifecycle with the criteria
// of its gates (registrysvc.WithGates), evaluated over its conditions.
type gatedLifecycles struct {
	mainGoals
	lc *domain.Lifecycle
}

func (l gatedLifecycles) Lifecycle(context.Context, string) (*domain.Lifecycle, error) {
	return l.lc, nil
}
func (l gatedLifecycles) Guard(_ context.Context, bb domain.Blackboard, expr, transition, d string) (bool, error) {
	return condition.CheckGuard(expr, bb, l.c.Conditions.Evaluate(bb).State, transition, d)
}
func (l gatedLifecycles) Gate(_ context.Context, bb domain.Blackboard, t domain.Transition, d string) (domain.GateResult, error) {
	return condition.CheckGate(t, bb, l.c.Conditions.Evaluate(bb).State, d)
}

func TestAGatePassesOnTheGoOfItsBoard(t *testing.T) {
	ctx := authz.With(context.Background(), admin)
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(gatedYAML))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	lc, err := registrysvc.WithGates(&maturity, cm.Gates())
	if err != nil {
		t.Fatal(err)
	}
	g.Lifecycles = gatedLifecycles{mainGoals{cm}, lc}
	g.DecisionPolicy = decision.Policy{}
	e.Methodologies = engine.StaticMethodologies{"gated": cm}

	p, err := e.Start(ctx, engine.StartRequest{Methodology: "gated", Goal: "delivery", BaselineID: base, ProjectID: testProject, Intent: "deliver it"})
	if err != nil {
		t.Fatal(err)
	}
	run := func() *engine.Process {
		t.Helper()
		if p, err = e.Run(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p = run(); p.Pending == nil || p.Pending.Action != "delivery/analysis" {
		t.Fatalf("the analysis first: %s %+v", p.Status, p.Pending)
	}
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "analysis"}}); err != nil {
		t.Fatal(err)
	}
	// the attested veto is asked before the decision opens
	if p = run(); p.Pending == nil || p.Pending.Action != methodology.AttestAction("delivery/analysis_gate", "compliant") {
		t.Fatalf("the attestation: %s %+v", p.Status, p.Pending)
	}
	if _, err = e.SubmitForm(ctx, p.ID, map[string]any{"verdict": "met", "comment": "checked"}); err != nil {
		t.Fatal(err)
	}
	// the decision is open: its board rules it, seat by seat
	if p = run(); p.Pending == nil || p.Pending.Action != methodology.RuleAction("delivery/analysis_gate") {
		t.Fatalf("the board rules: %s %+v", p.Status, p.Pending)
	}
	bb, err := g.BlackboardIn(ctx, p.ChangeID, "")
	if err != nil {
		t.Fatal(err)
	}
	points := domain.DecisionPointsOf(bb)
	if len(points) != 1 || points[0].Policy["gate"] != "delivery/analysis_gate" || !points[0].HumanOnly {
		t.Fatalf("one decision of the gate, for people: %+v", points)
	}
	point := points[0].ID
	if _, err := g.RuleDecision(ctx, p.ChangeID, graphapi.RuleRequest{Point: point, Outcome: domain.OutcomeDecided,
		Option: methodology.OptionGo, Confidence: 1, By: "user:x", Human: true, Role: "nobody"}); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("a role without a seat: %v", err)
	}
	rule := func(role string) {
		t.Helper()
		if _, err := e.SubmitForm(ctx, p.ID, map[string]any{"ruling": map[string]any{"point": point, "option": methodology.OptionGo, "role": role, "justification": "ready"}}); err != nil {
			t.Fatal(err)
		}
	}
	rule("contributor")
	if p = run(); p.Pending == nil || p.Pending.Action != methodology.RuleAction("delivery/analysis_gate") {
		t.Fatalf("unanimity waits for every seat: %s %+v", p.Status, p.Pending)
	}
	rule("approver")
	if p = run(); p.Pending == nil || p.Pending.Action != "delivery/designing" {
		t.Fatalf("passed, the design follows: %s %+v", p.Status, p.Pending)
	}
	c, err := g.Change(ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != "accepted" {
		t.Fatalf("the go took the transition: %s", c.State)
	}
	intent := "another intent"
	if _, err := g.UpdateChange(ctx, p.ChangeID, graph.ChangePatch{Intent: &intent}); !errors.Is(err, graph.ErrConflict) {
		t.Fatalf("the go froze the intent: %v", err)
	}
}
