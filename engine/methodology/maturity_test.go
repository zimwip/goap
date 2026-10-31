package methodology

import (
	"strings"
	"testing"
)

const maturityBase = `
name: m
version: 1.0.0
namespace: alm
roles: [{name: owner}]
conditions:
  - {name: ready, expr: 'artifacts.exists(x, x.target == "ready")'}
actions:
  - {name: prepare, kind: human, effects: {ready: true}}
`

// TestMaturityIssues checks what the compiler refuses of a maturity and of the gates moving along it (ADR 0179).
func TestMaturityIssues(t *testing.T) {
	cases := []struct {
		name, yaml, path string
	}{
		{"both", "lifecycle: maturity\nmaturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: b}`), "maturity"},
		{"no final", "maturity: {phases: [{name: a}, {name: b}]}\n" + process(`{enters: b}`), "maturity.phases"},
		{"first final", "maturity: {phases: [{name: a, final: true}, {name: b}]}\n" + process(`{enters: b}`), "maturity.phases[0].final"},
		{"exit not final", "maturity: {phases: [{name: a}, {name: b, final: true}, {name: c, exit: quit}]}\n" + process(`{enters: b}`), "maturity.phases[2].exit"},
		{"unknown phase", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: z}`), "processes[0].steps[1].gate.enters"},
		{"no enters", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{transition: go_on}`), "processes[0].steps[1].gate.enters"},
		{"back without phase", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: b, decision: {back: undo}}`), "processes[0].steps[1].gate.decision.back"},
		{"script without code", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: b, decision: {by: script}}`), "processes[0].steps[1].gate.decision.code"},
		{"script with a board", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: b, decision: {by: script, code: "x", board: [owner]}}`), "processes[0].steps[1].gate.decision.by"},
		{"unknown decider", "maturity: {phases: [{name: a}, {name: b, final: true}]}\n" + process(`{enters: b, decision: {by: robot}}`), "processes[0].steps[1].gate.decision.by"},
		{"phases with a domain lifecycle", "lifecycle: maturity\n" + process(`{transition: accept, enters: b}`), "processes[0].steps[1].gate.enters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse([]byte(maturityBase + tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			_, issues := m.CompileLenient()
			var paths []string
			for _, i := range issues {
				if i.Path == tc.path {
					return
				}
				paths = append(paths, i.Path+": "+i.Message)
			}
			t.Fatalf("no issue at %s: %s", tc.path, strings.Join(paths, "; "))
		})
	}
}

func process(gate string) string {
	return `processes:
  - name: p
    steps:
      - {name: prep, action: prepare}
      - name: g
        pre: {ready: true}
        roles: {accountable: owner}
        gate: ` + gate + "\n"
}

func TestMaturityGateDefaults(t *testing.T) {
	m, err := Parse([]byte(maturityBase + "maturity: {phases: [{name: a}, {name: b}, {name: c, final: true}]}\n" + process(`{enters: b, decision: {backTo: a}}`)))
	if err != nil {
		t.Fatal(err)
	}
	// backTo the phase it is in is refused: the gate is in a (the phase before b)
	if _, issues := m.CompileLenient(); !issues.HasErrors() {
		t.Fatal("back to the phase the gate is in is refused")
	}
	m, err = Parse([]byte(maturityBase + "maturity: {phases: [{name: a}, {name: b}, {name: c, final: true}]}\n" + process(`{enters: c, from: a, decision: {backTo: b}}`)))
	if err != nil {
		t.Fatal(err)
	}
	c, issues := m.CompileLenient()
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	g, ok := c.GateAt("p/g")
	if !ok || g.Transition != "g" || g.Back != "back_g" || g.From != "a" || g.Enters != "c" || g.BackTo != "b" || !g.Entry["ready"] {
		t.Fatalf("gate %+v", g)
	}
}
