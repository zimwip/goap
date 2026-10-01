package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A method composing its own steps (architecture plan "Activity concept": Method and MethodStep are Activities
// too) compiles exactly like a Process: it gets an agent and a goal of its own name, and its steps compile to
// actions the same way.
func TestMethodWithStepsCompilesLikeAProcess(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - name: assemble
    for: assembling
    steps:
      - {name: first, action: do_a}
      - {name: second, action: do_b, pre: {"step:assemble/first": true}}
processes:
  - name: flow
    steps:
      - {name: finish, method: assembling, pre: {a: true}}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ag, ok := c.Agent("assemble")
	if !ok || ag.ProcessOf() != "assemble" || ag.Planner != PlannerGOAP {
		t.Fatalf("method-generated agent: %+v, %v", ag, ok)
	}
	g, ok := c.Goal("assemble")
	if !ok || !g.Pre["b"] {
		t.Fatalf("method-generated goal: %+v, %v", g, ok)
	}
	acts := c.StepActions("assemble")
	if len(acts) != 2 {
		t.Fatalf("method step actions: %+v", acts)
	}
	me, ok := c.MethodByName("assemble")
	if !ok || me.ActorAgent() != "assemble" || me.Agent != "" {
		t.Fatalf("method: %+v, %v (ActorAgent falls back to the method's own name; Agent itself is untouched)", me, ok)
	}
	var bb domain.Blackboard
	choices := c.MethodsFor("assembling", bb)
	if len(choices) != 1 || choices[0].Name != "assemble" || choices[0].AgentGoal != "assemble" {
		t.Fatalf("choices: %+v", choices)
	}
	a, _ := c.Action("flow/finish")
	if a.Params["capability"] != "assembling" {
		t.Fatalf("flow step dispatches to the capability: %+v", a)
	}
}

// Exactly one of Agent or Steps; a method's own step may not name a capability (methods are not compiled yet
// while another method's steps are being walked, so a circular method->method resolution is refused, not
// silently wrong).
func TestMethodStepsValidation(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"both agent and steps": {`
methods: [{name: m, for: f, agent: worker, steps: [{name: s, action: do_a}]}]`, "not both"},
		"neither agent nor steps": {`
methods: [{name: m, for: f}]`, "names its actor"},
		"name collides with an agent": {`
methods: [{name: worker, for: f, steps: [{name: s, action: do_a}]}]`, "already an agent"},
		"name collides with a process": {`
methods: [{name: flow, for: f, steps: [{name: s, action: do_a}]}]
processes: [{name: flow, steps: [{name: s, action: do_a}]}]`, "already a process"},
		"name collides with a goal": {`
methods: [{name: all, for: f, steps: [{name: s, action: do_a}]}]`, "already a goal"},
		"a method step may not name a capability": {`
methods:
  - {name: inner, for: g, agent: worker}
  - name: outer
    for: f
    steps: [{name: s, method: g}]`, `no valid method provides "g"`},
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
