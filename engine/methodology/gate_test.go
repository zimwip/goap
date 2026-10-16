package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/builtins"
	"github.com/zimwip/goap/contracts/domain"
)

const gateBase = `
name: g
version: 1.0.0
namespace: alm
lifecycle: maturity
roles:
  - {name: lead}
  - {name: analyst}
  - {name: architect}
conditions:
  - {name: analysed, expr: 'artifacts.exists(x, x.target == "analysis")'}
  - {name: traced, expr: 'artifacts.exists(x, x.target == "trace")'}
  - {name: ruled, expr: 'artifacts.exists(x, x.target == "ruling")'}
actions:
  - {name: analyse, kind: human, effects: {analysed: true}}
  - {name: trace, kind: human, effects: {traced: true}}
  - {name: rule_gate, kind: human, effects: {ruled: true}}
`

func compileGate(t *testing.T, src string) (*Compiled, error) {
	t.Helper()
	m, err := Parse([]byte(gateBase + src))
	if err != nil {
		t.Fatal(err)
	}
	m.WithBuiltins(builtins.Known{})
	return m.Compile()
}

func TestGateCompiles(t *testing.T) {
	c, err := compileGate(t, `
processes:
  - name: delivery
    steps:
      - {name: analysis, action: analyse}
      - {name: tracing, action: trace}
      - name: analysis_gate
        pre: {analysed: true}
        roles: {accountable: lead}
        gate:
          transition: accept
          checklist:
            - {name: analysed, role: analyst, veto: true}
            - {name: traced, role: analyst}
            - {name: compliant, role: architect, attest: true, veto: true}
            - {name: traced, role: lead, when: 'change.criticality == "C3"'}
          decision: {board: [architect], rule: consent}
          freezes: {fields: [intent], types: [alm@Requirement]}
      - name: after
        pre: {"gate:delivery/analysis_gate": true}
        action: trace
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate item traced") {
		t.Fatalf("a duplicate item: %v", err)
	}
	c, err = compileGate(t, `
processes:
  - name: delivery
    steps:
      - {name: analysis, action: analyse}
      - {name: tracing, action: trace}
      - name: analysis_gate
        pre: {analysed: true}
        roles: {accountable: lead}
        gate:
          transition: accept
          checklist:
            - {name: analysed, role: analyst, veto: true}
            - {name: traced, role: analyst}
            - {name: compliant, role: architect, attest: true, veto: true}
          decision: {board: [architect], rule: consent}
          freezes: {fields: [intent], types: [alm@Requirement]}
      - name: after
        pre: {"gate:delivery/analysis_gate": true}
        action: trace
`)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := c.GateOf("accept")
	if !ok || g.Path != "delivery/analysis_gate" || g.Accountable != "lead" {
		t.Fatalf("gate: %+v", g)
	}
	if strings.Join(g.Board, ",") != "architect,lead" || g.Rule != "consent" {
		t.Fatalf("the accountable role sits on the board: %v %s", g.Board, g.Rule)
	}
	vetos, objectives := g.Criteria()
	if len(vetos) != 3 || len(objectives) != 1 || vetos[2].Name != "decision" || objectives[0].Expr != `world["traced"]` {
		t.Fatalf("criteria: %+v / %+v", vetos, objectives)
	}
	if vetos[1].Expr != `world["attested:delivery/analysis_gate/compliant"]` {
		t.Fatalf("an attested item checks its generated condition: %s", vetos[1].Expr)
	}
	open, ok := c.Action("delivery/analysis_gate:open")
	if !ok || open.Builtin != builtins.GateOpen || !open.Pre["analysed"] || !open.Pre["attested:delivery/analysis_gate/compliant"] || open.Pre["traced"] {
		t.Fatalf("the decision opens once the vetos hold: %+v", open)
	}
	pass, ok := c.Action("delivery/analysis_gate")
	if !ok || pass.Builtin != builtins.GatePass || !pass.Pre[GateGo("delivery/analysis_gate")] || !pass.Effects[GatePassed("delivery/analysis_gate")] {
		t.Fatalf("the gate passes once its decision said go: %+v", pass)
	}
	attest, ok := c.Action(AttestAction("delivery/analysis_gate", "compliant"))
	if !ok || attest.Kind != KindHuman || attest.Roles[0] != "architect" || attest.Form == nil || attest.Form.Artifact != "attestation:delivery/analysis_gate/compliant" {
		t.Fatalf("attest: %+v", attest)
	}
	// the conditions of the gate
	bb := domain.Blackboard{Change: domain.Change{ID: "c1", State: "accepted"}}
	world := c.Conditions.Evaluate(bb).State
	if world[GatePassed("delivery/analysis_gate")] || world[AttestedCondition("delivery/analysis_gate", "compliant")] {
		t.Fatalf("nothing passed nor attested yet: %v", world)
	}
	bb.Change.Items = []domain.ChangeItem{
		{ID: "t1", Type: domain.TypeTransition, Data: map[string]any{"transition": "accept", "from": "draft", "to": "accepted"}},
		{ID: "a1", Type: domain.TypeArtifact, Target: "attestation:delivery/analysis_gate/compliant", Data: map[string]any{"verdict": "met"}},
	}
	world = c.Conditions.Evaluate(bb).State
	if !world[GatePassed("delivery/analysis_gate")] || !world[AttestedCondition("delivery/analysis_gate", "compliant")] {
		t.Fatalf("passed and attested: %v", world)
	}
	bb.Change.Items = append(bb.Change.Items,
		domain.ChangeItem{ID: "t2", Type: domain.TypeTransition, Data: map[string]any{"transition": "start", "from": "accepted", "to": "draft"}},
		domain.ChangeItem{ID: "a2", Type: domain.TypeArtifact, Target: "attestation:delivery/analysis_gate/compliant", Data: map[string]any{"verdict": "not_met"}})
	world = c.Conditions.Evaluate(bb).State
	if world[GatePassed("delivery/analysis_gate")] || world[AttestedCondition("delivery/analysis_gate", "compliant")] {
		t.Fatalf("gone back, attestation withdrawn: %v", world)
	}
}

func TestGateRefusals(t *testing.T) {
	for want, src := range map[string]string{
		"accountable role of its own": `
processes:
  - name: p
    steps:
      - {name: g, gate: {transition: accept}}`,
		"already the gate": `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept}}
      - {name: h, roles: {accountable: lead}, gate: {transition: accept}}`,
		"does no work": `
processes:
  - name: p
    steps:
      - {name: g, action: analyse, roles: {accountable: lead}, gate: {transition: accept}}`,
		`unknown role "nobody"`: `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept, decision: {board: [nobody]}}}`,
		"unanimity, majority or consent": `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept, decision: {board: [lead], rule: vote}}}`,
		`unknown condition "missing"`: `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept, checklist: [{name: missing}]}}`,
		"freezes title, intent or description": `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept, freezes: {fields: [owner]}}}`,
		"a method says how, it has no gate": `
processes:
  - name: p
    steps:
      - {name: s, method: decide}
methods:
  - name: decide
    for: decide
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept}}`,
	} {
		if _, err := compileGate(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	m, err := Parse([]byte(strings.Replace(gateBase, "lifecycle: maturity\n", "", 1) + `
processes:
  - name: p
    steps:
      - {name: g, roles: {accountable: lead}, gate: {transition: accept}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Compile(); err == nil || !strings.Contains(err.Error(), "names none (lifecycle)") {
		t.Errorf("a gate without a lifecycle: %v", err)
	}
}

func TestGateDecidedByAMethod(t *testing.T) {
	c, err := compileGate(t, `
processes:
  - name: p
    steps:
      - {name: g, method: gate_decision, roles: {accountable: lead}, gate: {transition: accept}}
methods:
  - name: board
    for: gate_decision
    actions: [rule_gate]
    done: {ruled: true}
`)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := c.Action("p/g:decide")
	if !ok || d.Kind != KindCall || d.Call.Capability != "gate_decision" || !d.Pre[GateOpen("p/g")] || !d.Effects[GateGo("p/g")] {
		t.Fatalf("the decision method runs once the decision is open, towards its go: %+v", d)
	}
	if g, _ := c.GateAt("p/g"); g.Method != "gate_decision" {
		t.Fatalf("gate: %+v", g)
	}
}
