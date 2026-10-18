package methodology

import (
	"testing"
)

const rolesSrc = `
name: roles
namespace: alm
goal: p
roles: [{name: analyst}, {name: owner}, {name: dev}, {name: tester}]
conditions: [{name: a, expr: "false"}, {name: b, expr: "false"}, {name: c, expr: "false"}, {name: d, expr: "false"}]
actions:
  - {name: frame, kind: human, effects: {a: true}}
  - {name: code, kind: llm, prompt: x, pre: {a: true}, effects: {b: true}}
  - {name: code_go, kind: llm, prompt: x, specializes: code, when: "true"}
  - {name: check, kind: human, role: tester, pre: {b: true}, effects: {c: true}}
  - {name: pooled, kind: llm, prompt: x, pre: {c: true}, effects: {d: true}}
methods:
  - {name: build_it, for: build, roles: {responsible: dev}, actions: [pooled], done: {d: true}}
processes:
  - name: p
    roles: {responsible: analyst, accountable: owner}
    steps:
      - {name: framing, action: frame}
      - name: build
        roles: {responsible: dev}
        steps:
          - {name: code, action: code}
          - {name: check, action: check}
      - {name: finish, method: build, pre: {c: true}}
`

// ADR 0136: the steps inherit the roles of their process field by field, the agents act as the responsible role of
// what they run, the actions as the role of what refines them, a specialization as the action it specializes.
func TestRolesPropagate(t *testing.T) {
	m, err := Parse([]byte(rolesSrc))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	framing, _ := c.StepByPath("p/framing")
	if r := framing.Effective; r == nil || r.Responsible != "analyst" || r.Accountable != "owner" {
		t.Fatalf("framing inherits the process: %+v", r)
	}
	code, _ := c.StepByPath("p/build/code")
	if r := code.Effective; r == nil || r.Responsible != "dev" || r.Accountable != "owner" {
		t.Fatalf("a sub-step keeps what its step leaves unsaid: %+v", r)
	}
	for name, want := range map[string]string{"p/framing": "analyst", "p/build/code": "dev", "p/build/check": "tester",
		"frame": "analyst", "code": "dev", "code_go": "dev", "check": "tester", "pooled": "dev"} {
		if a, ok := c.Action(name); !ok || a.Role != want {
			t.Errorf("action %s acts as %q, want %q", name, a.Role, want)
		}
	}
	for name, want := range map[string]string{"p": "analyst", "build_it": "dev"} {
		if ag, ok := c.Agent(name); !ok || ag.Role != want {
			t.Errorf("agent %s acts as %q, want %q", name, ag.Role, want)
		}
	}
}

// A process or a method naming no responsible role is warned while written, refused once stored.
func TestResponsibleIsRequired(t *testing.T) {
	m, err := Parse([]byte(rolesSrc))
	if err != nil {
		t.Fatal(err)
	}
	m.Processes[0].Roles = &Responsibilities{Accountable: "owner"}
	m.Methods[0].Roles = nil
	issues := m.Validate()
	if issues.HasErrors() || len(issues.Warnings()) != 2 {
		t.Fatalf("a draft is warned: %v", issues)
	}
	stored := m.ValidateStored()
	paths := map[string]bool{}
	for _, i := range stored.Errors() {
		paths[i.Path] = true
	}
	if !paths["processes[0].roles.responsible"] || !paths["methods[0].roles.responsible"] {
		t.Fatalf("a stored methodology is refused: %v", stored)
	}
}
