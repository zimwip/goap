package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/builtins"
)

const controlBase = `
name: ctl
version: 1.0.0
namespace: alm
roles:
  - {name: dev}
conditions:
  - name: architecture_decided
    description: The architecture decisions are collected (there may be none)
    expr: 'artifacts.exists(x, x.target == "architecture")'
    control:
      types: [alm@ArchitectureDecision]
      via: alm@governs
      where: 'self.state == "accepted"'
  - name: method_rules
    control:
      rules:
        - {id: TESTS, statement: Every changed function is tested, check: {language: shell, code: "go test ./..."}}
        - {id: NAMES, statement: Names say the business concept, severity: advice}
  - {name: implemented, expr: 'artifacts.exists(x, x.target == "code")'}
  - {name: decided, expr: 'artifacts.exists(x, x.target == "architecture")'}
actions:
  - {name: decide, kind: human, effects: {decided: true}}
  - {name: implement, kind: llm, prompt: code, effects: {implemented: true}, controls: [method_rules]}
`

func compileControl(t *testing.T, src string) (*Compiled, error) {
	t.Helper()
	m, err := Parse([]byte(controlBase + src))
	if err != nil {
		t.Fatal(err)
	}
	m.WithBuiltins(builtins.Known{})
	return m.Compile()
}

// A control is a second input (ADR 0162): an activity waits for its rules to be collected, and its executions find
// the controls of the action and of the steps containing it.
func TestControlsAreWaitedForAndFound(t *testing.T) {
	c, err := compileControl(t, `
processes:
  - name: delivery
    roles: {responsible: dev}
    steps:
      - {name: design, action: decide}
      - name: build
        controls: [architecture_decided]
        steps:
          - {name: code, action: implement}
`)
	if err != nil {
		t.Fatal(err)
	}
	entry, _, ok := c.StepCriteria("delivery/build/code")
	if !ok || !entry["architecture_decided"] {
		t.Fatalf("the sub-step waits for the control of its step: entry %v", entry)
	}
	a, _ := c.Action("implement")
	if !a.Pre["method_rules"] {
		t.Fatalf("the action waits for its own control: pre %v", a.Pre)
	}
	if d := c.Definition().Actions[1]; d.Pre != nil {
		t.Fatalf("the definition is not modified: %v", d.Pre)
	}
	var names []string
	for _, ci := range c.ControlsOf("implement", "delivery/build/code") {
		names = append(names, ci.Condition)
	}
	if strings.Join(names, ",") != "method_rules,architecture_decided" {
		t.Fatalf("controls of the execution: %v", names)
	}
	ci, _ := c.ControlCondition("architecture_decided")
	if ci.Via != "alm@governs" || len(ci.Types) != 1 {
		t.Fatalf("compiled control: %+v", ci)
	}
	if !ci.Where.Keeps(map[string]any{"state": "accepted"}, nil, nil) || ci.Where.Keeps(map[string]any{"state": "draft"}, nil, nil) {
		t.Fatal("the where keeps the accepted rules only")
	}
	mr, _ := c.ControlCondition("method_rules")
	if len(mr.Rules) != 2 || mr.Rules[1].SeverityOf() != SeverityAdvice || mr.Rules[0].SeverityOf() != SeverityVeto {
		t.Fatalf("general rules: %+v", mr.Rules)
	}
}

func TestControlIssues(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"not a control", `
processes:
  - name: p
    roles: {responsible: dev}
    steps:
      - {name: s, action: decide, controls: [implemented]}
`, "carries no control"},
		{"unknown", `
processes:
  - name: p
    roles: {responsible: dev}
    steps:
      - {name: s, action: decide, controls: [nothing]}
`, "unknown condition nothing"},
		{"empty control", `
  - {name: empty_ctl, control: {}}
`, "a control covers rules"},
		{"scoped", `
  - {name: scoped_ctl, scope: impact, expr: 'self.key != ""', control: {types: [alm@CodingRule]}}
`, "a control is about the change"},
		{"bad rule", `
  - {name: bad_rules, control: {rules: [{id: X, statement: "", severity: fatal, check: {language: python, code: x}}]}}
`, "statement required"},
		{"bad where", `
  - {name: bad_where, control: {types: [alm@CodingRule], where: 'self.state +'}}
`, "where"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(strings.TrimSpace(tc.src), "processes") {
				// a condition: insert it among the conditions
				m, err := Parse([]byte(strings.Replace(controlBase, "actions:\n", strings.TrimPrefix(tc.src, "\n")+"actions:\n", 1)))
				if err != nil {
					t.Fatal(err)
				}
				m.WithBuiltins(builtins.Known{})
				_, err = m.Compile()
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
				return
			}
			_, err := compileControl(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
