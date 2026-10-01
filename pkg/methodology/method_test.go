package methodology

import (
	"slices"
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
		"goal with steps": {`
methods: [{name: m, for: f, agent: worker, goal: all, steps: [{name: s, action: do_a}]}]`, "goal applies"},
		"unknown agent with steps": {`
methods: [{name: m, for: f, agent: nobody, steps: [{name: s, action: do_a}]}]`, "unknown"},
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

// The agent performs the method: it gets the activities that compose it and the goal they reach, and is the actor.
func TestAgentPerformsAMethodComposingSteps(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - name: assemble
    for: assembling
    agent: worker
    steps:
      - {name: first, action: do_a}
      - {name: second, action: do_b}
processes:
  - name: flow
    steps:
      - {name: finish, method: assembling}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	me, _ := c.MethodByName("assemble")
	if me.ActorAgent() != "worker" || c.MethodGoal("assemble") != "assemble" {
		t.Fatalf("actor %s goal %s", me.ActorAgent(), c.MethodGoal("assemble"))
	}
	w, _ := c.Agent("worker")
	if !slices.Contains(w.Goals, "assemble") || !slices.Contains(w.Actions, "assemble/first") || !slices.Contains(w.Actions, "do_c") {
		t.Fatalf("worker: %+v", w)
	}
	if _, ok := c.Agent("assemble"); ok {
		t.Fatal("no agent is generated when the method names its actor")
	}
	ls, _ := c.CheckLevels("assemble")
	if len(ls) != 1 || ls[0].Agent != "worker" || ls[0].Goal != "assemble" || len(ls[0].Steps) != 2 {
		t.Fatalf("levels: %+v", ls)
	}
}
