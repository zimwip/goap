package enginee2e

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/platform/registrysvc"
)

// matured states its maturity (ADR 0179): no domain names its lifecycle, its gates move the change between its phases.
// The analysis gate is decided by a person, the design gate by a script.
const maturedYAML = `
name: matured
version: 1.0.0
namespace: alm
goal: delivery
roles:
  - {name: contributor}
  - {name: approver}
maturity:
  phases:
    - {name: analysing}
    - {name: designing}
    - {name: delivered, final: true}
    - {name: abandoned, final: true, exit: abandon}
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
          enters: designing
      - name: design
        pre: {"gate:delivery/analysis_gate": true}
        action: design
      - name: design_gate
        pre: {"gate:delivery/analysis_gate": true, designed: true}
        roles: {accountable: approver}
        gate:
          enters: delivered
          decision:
            by: script
            backTo: analysing
            code: |
              const gate = ctx.param("gate");
              const point = ctx.decisionPoints().find(d => d.policy.gate === gate && d.status !== "decided");
              ctx.decide(point.id, "go", 1, "the design is recorded");
`

func TestTheMaturityDerivesTheLifecycle(t *testing.T) {
	m, err := methodology.Parse([]byte(maturedYAML))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	lc := cm.Lifecycle()
	if lc == nil || lc.Name != "matured" || lc.Initial != "analysing" || len(lc.States) != 4 {
		t.Fatalf("lifecycle %+v", lc)
	}
	want := map[string][2]string{"analysis_gate": {"analysing", "designing"}, "design_gate": {"designing", "delivered"},
		"back_design_gate": {"designing", "analysing"}}
	abandons := 0
	for _, tr := range lc.Transitions {
		if tr.Name == "abandon" {
			abandons++
			continue
		}
		w, ok := want[tr.Name]
		if !ok || tr.From != w[0] || tr.To != w[1] {
			t.Fatalf("transition %+v", tr)
		}
		delete(want, tr.Name)
	}
	if len(want) != 0 || abandons != 2 {
		t.Fatalf("missing %v, abandons %d: %+v", want, abandons, lc.Transitions)
	}
	gated, err := registrysvc.WithGates(lc, cm.Gates())
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range gated.Transitions {
		if tr.Name == "design_gate" {
			names := map[string]bool{}
			for _, v := range tr.Vetos {
				names[v.Name] = true
			}
			// the guard of the transition is the entry of its gate, and its decision
			if !names["entry:designed"] || !names["entry:gate:delivery/analysis_gate"] || !names["decision"] {
				t.Fatalf("vetos of the design gate: %+v", tr.Vetos)
			}
		}
	}
}

func TestAGateOfTheMaturityDecidedByAPersonThenAScript(t *testing.T) {
	ctx := authz.With(context.Background(), admin)
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(maturedYAML))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	lc, err := registrysvc.WithGates(cm.Lifecycle(), cm.Gates())
	if err != nil {
		t.Fatal(err)
	}
	g.Lifecycles = gatedLifecycles{mainGoals{cm}, lc}
	g.DecisionPolicy = decision.Policy{}
	e.Methodologies = engine.StaticMethodologies{"matured": cm}
	e.Executors[methodology.KindScript] = engine.ScriptExecutor{Sandboxes: engine.InprocSandboxes{}}

	p, err := e.Start(ctx, engine.StartRequest{Methodology: "matured", Goal: "delivery", ForkCommit: base, ProjectID: testProject, Intent: "deliver it"})
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
	state := func() string {
		t.Helper()
		c, err := g.Change(ctx, p.ChangeID)
		if err != nil {
			t.Fatal(err)
		}
		return c.State
	}
	if p = run(); p.Pending == nil || p.Pending.Action != "delivery/analysis" {
		t.Fatalf("the analysis first: %s %+v", p.Status, p.Pending)
	}
	if s := state(); s != "analysing" {
		t.Fatalf("a change starts in the first phase: %s", s)
	}
	// the go of a gate is its decision: no move by hand without it
	if _, err := g.TransitionChange(ctx, p.ChangeID, graphapi.TransitionRequest{Transition: "analysis_gate"}); !errors.Is(err, graph.ErrConflict) && !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("a move with no decision: %v", err)
	}
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "analysis"}}); err != nil {
		t.Fatal(err)
	}
	if p = run(); p.Pending == nil || p.Pending.Action != methodology.RuleAction("delivery/analysis_gate") {
		t.Fatalf("a person rules the analysis gate: %s %+v", p.Status, p.Pending)
	}
	bb, err := g.BlackboardIn(ctx, p.ChangeID, "")
	if err != nil {
		t.Fatal(err)
	}
	var point string
	for _, d := range bb.Change.DecisionPointsAt(bb.At, nil) {
		if d.Policy["gate"] == "delivery/analysis_gate" {
			point = d.ID
		}
	}
	if _, err := e.SubmitForm(ctx, p.ID, map[string]any{"ruling": map[string]any{"point": point, "option": methodology.OptionGo, "role": "approver", "justification": "ready"}}); err != nil {
		t.Fatal(err)
	}
	if p = run(); p.Pending == nil || p.Pending.Action != "delivery/design" {
		t.Fatalf("passed, the design follows: %s %+v", p.Status, p.Pending)
	}
	if s := state(); s != "designing" {
		t.Fatalf("the go entered the next phase: %s", s)
	}
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "design"}}); err != nil {
		t.Fatal(err)
	}
	// the design gate is ruled by its script: nobody is asked, the change is delivered
	if p = run(); p.Pending != nil {
		bb, _ := g.BlackboardIn(ctx, p.ChangeID, "")
		for _, d := range bb.Change.DecisionPointsAt(bb.At, nil) {
			t.Logf("point %+v", d)
		}
		for _, s := range p.Steps {
			t.Logf("step %+v", s)
		}
		t.Fatalf("the script decides alone: %s %+v", p.Status, p.Pending)
	}
	if s := state(); s != "delivered" {
		t.Fatalf("the script's go delivered the change: %s (%s)", s, p.Status)
	}
}
