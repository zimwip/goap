package methodology

import (
	"strings"
	"testing"
)

const processBase = `
name: p
version: 1.0.0
namespace: alm
conditions:
  - {name: a, expr: 'artifacts.exists(x, x.type == "a")'}
  - {name: b, expr: 'artifacts.exists(x, x.type == "b")'}
  - {name: c, expr: 'artifacts.exists(x, x.type == "c")'}
actions:
  - {name: do_a, kind: human, effects: {a: true}}
  - {name: do_b, kind: human, pre: {a: true}, effects: {b: true}}
  - {name: do_c, kind: human, effects: {c: true}}
goals:
  - {name: all, pre: {c: true}}
agents:
  - {name: worker, actions: [do_c], goals: [all]}
`

func compileProcess(t *testing.T, processes string) (*Compiled, Issues) {
	t.Helper()
	m, err := Parse([]byte(processBase + processes))
	if err != nil {
		t.Fatal(err)
	}
	return m.compile()
}

func TestProcessCompilesToStepActions(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: flow
    steps:
      - name: phase
        parallel: true
        steps:
          - {name: first, action: do_a}
          - {name: other, actions: [do_c, do_a]}
          - {name: second, action: do_b, after: [first]}
      - {name: agentic, agent: worker}
      - {name: manual, description: Sign off}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ag, ok := c.Agent("flow")
	if !ok || ag.ProcessOf() != "flow" {
		t.Fatalf("no agent for the process: %+v", ag)
	}
	var names []string
	for _, a := range c.AgentActions(ag) {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "flow/phase/first,flow/phase/other:do_c,flow/phase/other:do_a,flow/phase/second,flow/agentic,flow/manual" {
		t.Fatalf("step actions %s", got)
	}
	second, _ := c.Action("flow/phase/second")
	if second.Implements != "do_b" || !second.Pre["a"] || len(second.Pre) != 1 || second.Declared() != "do_b" {
		t.Fatalf("an action step keeps its action and waits for the steps it follows: %+v", second)
	}
	other, _ := c.Action("flow/phase/other:do_a")
	if len(other.Pre) != 0 || !other.Effects["c"] || !other.Effects["a"] || other.Declared() != "do_a" {
		t.Fatalf("a parallel step waits for nothing; each alternative reaches the step's criteria (the first's effects): %+v", other)
	}
	agentic, _ := c.Action("flow/agentic")
	if agentic.Builtin != BuiltinStep || agentic.Params["goal"] != "all" || !agentic.Pre["a"] || !agentic.Pre["b"] || !agentic.Pre["c"] || !agentic.Effects["c"] {
		t.Fatalf("an agent step follows the phase and is done by the agent's goal: %+v", agentic)
	}
	manual, _ := c.Action("flow/manual")
	if manual.Kind != KindHuman || manual.Instructions != "Sign off" || !manual.Effects[StepCondition("flow/manual")] {
		t.Fatalf("a manual step is a human task done once submitted: %+v", manual)
	}
	g, ok := c.Goal("flow")
	if !ok || !g.Pre[StepCondition("flow/manual")] || !g.Pre["c"] {
		t.Fatalf("the goal of the process is every step done: %+v", g)
	}
	if !c.Conditions.Has(StepCondition("flow/manual")) {
		t.Fatal("the condition of the manual step is not compiled")
	}
	// the declared agents do not plan with the step actions
	w, _ := c.Agent("worker")
	for _, a := range c.AgentActions(w) {
		if strings.Contains(a.Name, "/") {
			t.Fatalf("worker plans with a step: %s", a.Name)
		}
	}
	if list := c.AgentList(); len(list) != 2 || list[1].Name != "flow" {
		t.Fatalf("agents %+v", list)
	}
}

func TestProcessValidation(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"two methods": {`
processes:
  - name: x
    steps: [{name: s, action: do_a, agent: worker}]`, "one method"},
		"unknown action": {`
processes:
  - name: x
    steps: [{name: s, action: nope}]`, `unknown action "nope"`},
		"after in a sequence": {`
processes:
  - name: x
    steps: [{name: s, action: do_a}, {name: t, action: do_c, after: [s]}]`, "after applies"},
		"after a later step": {`
processes:
  - name: x
    parallel: true
    steps: [{name: s, action: do_a, after: [t]}, {name: t, action: do_c}]`, "not an earlier step"},
		"nesting itself": {`
processes:
  - name: x
    steps: [{name: s, process: y}]
  - name: y
    steps: [{name: s, process: x}]`, "nests itself"},
		"unknown process": {`
processes:
  - name: x
    steps: [{name: s, process: nope}]`, `unknown process "nope"`},
		"name of an agent": {`
processes:
  - name: worker
    steps: [{name: s}]`, "already an agent"},
		"conflicting conditions": {`
processes:
  - name: x
    steps: [{name: s, action: do_a}, {name: t, action: do_c, pre: {a: false}}]`, "both true and false"},
		"goal without agent": {`
processes:
  - name: x
    steps: [{name: s, action: do_a, goal: all}]`, "goal applies"},
		"no steps": {`
processes:
  - name: x
    steps: []`, "at least one step"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, issues := compileProcess(t, tc.yaml)
			if !strings.Contains(issues.Error(), tc.want) {
				t.Fatalf("want %q in %v", tc.want, issues)
			}
		})
	}
}

func TestProcessOnlyMethodology(t *testing.T) {
	m, err := Parse([]byte(`
name: q
version: 1.0.0
namespace: alm
processes:
  - name: checklist
    steps: [{name: one}, {name: two, process: other/flow}]
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if list := c.AgentList(); len(list) != 1 || list[0].Name != "checklist" {
		t.Fatalf("a methodology of processes only has their agents: %+v", list)
	}
	two, _ := c.Action("checklist/two")
	if two.Params["methodology"] != "other" || two.Params["agent"] != "flow" || !two.Pre[StepCondition("checklist/one")] {
		t.Fatalf("nested process of another methodology: %+v", two)
	}
}

func TestNestedProcessIsDoneByItsCriteria(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: outer
    steps:
      - {name: inner_run, process: inner}
      - {name: last, action: do_b}
  - name: inner
    steps:
      - {name: first, action: do_a}
      - {name: sign}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	run, _ := c.Action("outer/inner_run")
	if !run.Effects["a"] || !run.Effects[StepCondition("inner/sign")] {
		t.Fatalf("a nested process of the methodology is done by its exit criteria: %+v", run.Effects)
	}
	outer, _ := c.Agent("outer")
	var names []string
	for _, a := range c.AgentActions(outer) {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "outer/inner_run,outer/last" {
		t.Fatalf("the outer agent plans with its own steps only: %s", got)
	}
	if list := c.AgentList(); list[1].Name != "outer" || list[2].Name != "inner" {
		t.Fatalf("process agents in declaration order: %+v", list)
	}
}
