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
  - {name: method_known, expr: 'change.id != ""'}
  - {name: implemented, expr: 'artifacts.exists(x, x.target == "code")'}
actions:
  - {name: decide, kind: human, effects: {architecture_decided: true}}
  - name: implement
    kind: llm
    prompt: code
    effects: {implemented: true}
    controls:
      - condition: method_known
        rules:
          - {id: TESTS, statement: Every changed function is tested, check: {language: shell, code: "go test ./..."}}
          - {id: NAMES, statement: Names say the business concept, severity: advice}
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
        controls:
          - {condition: architecture_decided, types: [alm@ArchitectureDecision], via: alm@governs, where: 'self.state == "accepted"'}
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
	if !a.Pre["method_known"] {
		t.Fatalf("the action waits for its own control: pre %v", a.Pre)
	}
	if d := c.Definition().Actions[1]; d.Pre != nil {
		t.Fatalf("the definition is not modified: %v", d.Pre)
	}
	var names []string
	for _, ci := range c.ControlsOf("implement", "delivery/build/code") {
		names = append(names, ci.Condition)
	}
	if strings.Join(names, ",") != "method_known,architecture_decided" {
		t.Fatalf("controls of the execution: %v", names)
	}
	cs := c.ControlsOf("implement", "delivery/build/code")
	ci, mr := cs[1], cs[0]
	if ci.Via != "alm@governs" || len(ci.Types) != 1 || ci.Description == "" {
		t.Fatalf("compiled control: %+v", ci)
	}
	if !ci.Where.Keeps(map[string]any{"state": "accepted"}, nil, nil) || ci.Where.Keeps(map[string]any{"state": "draft"}, nil, nil) {
		t.Fatal("the where keeps the accepted rules only")
	}
	if len(mr.Rules) != 2 || mr.Rules[1].SeverityOf() != SeverityAdvice || mr.Rules[0].SeverityOf() != SeverityVeto {
		t.Fatalf("general rules: %+v", mr.Rules)
	}
}

func TestControlIssues(t *testing.T) {
	step := func(controls string) string {
		return `
processes:
  - name: p
    roles: {responsible: dev}
    steps:
      - name: s
        action: decide
        controls: ` + controls + "\n"
	}
	for _, tc := range []struct{ name, src, want string }{
		{"nothing covered", step("[implemented]"), "a control covers rules"},
		{"unknown", step("[{condition: nothing, types: [alm@CodingRule]}]"), "unknown condition nothing"},
		{"no condition", step("[{types: [alm@CodingRule]}]"), "condition required"},
		{"scoped", `
  - {name: per_impact, scope: impact, expr: 'self.key != ""'}
` + step("[{condition: per_impact, types: [alm@CodingRule]}]"), "about the change"},
		{"bad rule", step(`[{condition: implemented, rules: [{id: X, statement: "", severity: fatal, check: {language: python, code: x}}]}]`), "statement required"},
		{"bad where", step(`[{condition: implemented, types: [alm@CodingRule], where: 'self.state +'}]`), "where"},
		{"via without types", step("[{condition: implemented, via: alm@governs, rules: [{id: X, statement: x}]}]"), "name their types"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if cond, procs, ok := strings.Cut(src, "\nprocesses:"); ok && strings.TrimSpace(cond) != "" {
				// a condition of its own: insert it among the conditions
				src = "\nprocesses:" + procs
				base := strings.Replace(controlBase, "actions:\n", strings.TrimPrefix(cond, "\n")+"\nactions:\n", 1)
				m, err := Parse([]byte(base + src))
				if err != nil {
					t.Fatal(err)
				}
				m.WithBuiltins(builtins.Known{})
				if _, err = m.Compile(); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
				return
			}
			_, err := compileControl(t, src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// A condition is never marked a control: it is one by its use (ADR 0162). The same condition is the output of the
// activity collecting the rules and the control of another; a condition carries no control of its own.
func TestAConditionIsAControlByItsUse(t *testing.T) {
	if _, err := Parse([]byte(strings.Replace(controlBase, "    expr: 'artifacts.exists(x, x.target == \"architecture\")'\n",
		"    expr: 'artifacts.exists(x, x.target == \"architecture\")'\n    control: {types: [alm@CodingRule]}\n", 1))); err == nil {
		t.Fatal("a condition declares no control")
	}
	c, err := compileControl(t, `
processes:
  - name: delivery
    roles: {responsible: dev}
    steps:
      - {name: design, action: decide}
      - name: build
        action: implement
        controls: [{condition: architecture_decided, types: [alm@CodingRule]}]
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ControlsOf("decide", "delivery/design")) != 0 || len(c.ControlsOf("implement", "delivery/build")) != 2 {
		t.Fatalf("the design produces the condition, the build is controlled by it: %v", c.ControlsOf("implement", "delivery/build"))
	}
}
