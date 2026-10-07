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
      - {name: finish, method: assembling, pre: {c: true}}
`)
	if issues.HasErrors() {
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
	if !ok || me.ActorAgent() != "assemble" {
		t.Fatalf("method: %+v, %v (its agent is generated, of its name)", me, ok)
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

// A method needs actions or steps; a method of actions alone states what it reaches; a method step may not name a
// capability (methods are not compiled yet while another method's steps are being walked, so a circular
// method->method resolution is refused, not silently wrong).
func TestMethodStepsValidation(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"done with steps": {`
methods: [{name: m, for: f, done: {c: true}, steps: [{name: s, action: do_a}]}]`, "done applies"},
		"unknown action with steps": {`
methods: [{name: m, for: f, actions: [nobody], steps: [{name: s, action: do_a}]}]`, "unknown action"},
		"neither actions nor steps": {`
methods: [{name: m, for: f}]`, "declare the actions"},
		"llm planner needs a model": {`
methods: [{name: m, for: f, planner: llm, actions: [do_c], done: {c: true}}]`, "model is required"},
		"name collides with an agent": {`
methods: [{name: worker, for: f, steps: [{name: s, action: do_a}]}]`, "already an agent"},
		"name collides with a process": {`
methods: [{name: flow, for: f, steps: [{name: s, action: do_a}]}]
processes: [{name: flow, steps: [{name: s, action: do_a}]}]`, "already a process"},
		"name collides with a goal": {`
methods: [{name: all, for: f, steps: [{name: s, action: do_a}]}]`, "already a goal"},
		"a method step may not name a capability": {`
methods:
  - {name: inner, for: g, actions: [do_c], done: {c: true}}
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

// The agent applying a method is generated from it: its pool is the method's actions plus those of its steps, its
// planner, model and role come from the method.
func TestMethodGeneratesItsAgent(t *testing.T) {
	c, issues := compileProcess(t, `
roles: [{name: builder}]
methods:
  - name: assemble
    for: assembling
    planner: hybrid
    roles: {responsible: builder}
    actions: [do_c]
    steps:
      - {name: first, action: do_a}
      - {name: second, action: do_b}
processes:
  - name: flow
    steps:
      - {name: finish, method: assembling}
`)
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	ag, ok := c.Agent("assemble")
	if !ok || ag.Planner != PlannerHybrid || ag.Role != "builder" || !slices.Contains(ag.Goals, "assemble") ||
		!slices.Contains(ag.Actions, "assemble/first") || !slices.Contains(ag.Actions, "do_c") {
		t.Fatalf("agent: %+v, %v", ag, ok)
	}
	ls, _ := c.CheckLevels("assemble")
	if len(ls) != 3 || ls[0].Agent != "assemble" || ls[0].Goal != "assemble" || len(ls[0].Steps) != 2 || ls[1].Kind != LevelAction {
		t.Fatalf("levels: %+v", ls)
	}
}

// Among the methods whose context holds, the highest priority wins, then the most specific context.
func TestMethodsRankBySpecificity(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - {name: generic, for: f, actions: [do_c], done: {c: true}}
  - {name: one, for: f, actions: [do_c], done: {c: true}, when: 'artifacts.exists(x, x.type == "u")'}
  - {name: two, for: f, actions: [do_c], done: {c: true}, when: 'artifacts.exists(x, x.type == "u") && artifacts.exists(x, x.type == "v")'}
  - {name: forced, for: f, actions: [do_c], done: {c: true}, priority: 1, when: 'artifacts.exists(x, x.type == "u")'}
`)
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	var bb domain.Blackboard
	bb.Change.Items = []domain.ChangeItem{{ID: "1", Kind: domain.KindArtifact, Type: "u"}, {ID: "2", Kind: domain.KindArtifact, Type: "v"}}
	var names []string
	for _, m := range c.MethodsFor("f", bb) {
		names = append(names, m.Name)
	}
	if want := []string{"forced", "two", "one", "generic"}; !slices.Equal(names, want) {
		t.Fatalf("order %v, want %v", names, want)
	}
}
