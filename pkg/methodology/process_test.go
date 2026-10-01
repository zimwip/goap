package methodology

import (
	"github.com/zimwip/goap/pkg/domain"
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
methods:
  - {name: by_worker, for: working, agent: worker}
processes:
  - name: flow
    references: [{title: Guide, ref: "document-repository:flow.md"}]
    steps:
      - name: phase
        pre: {c: false}
        steps:
          - {name: first, action: do_a}
          - {name: other, actions: [do_c, do_a], pre: {"step:flow/manual": true}}
          - {name: second, action: do_b}
      - {name: agentic, method: working, pre: {b: true}}
      - name: manual
        description: Sign off
        references: [{title: Checklist, ref: "doc:CHK-1", section: "2"}]
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
	// no implicit order: a step needs what the phase needs, its own entry conditions and its action's preconditions
	first, _ := c.Action("flow/phase/first")
	if len(first.Pre) != 1 || first.Pre["c"] {
		t.Fatalf("the first step only inherits the phase's entry: %+v", first.Pre)
	}
	second, _ := c.Action("flow/phase/second")
	if second.Implements != "do_b" || !second.Pre["a"] || second.Pre["c"] || len(second.Pre) != 2 || second.Declared() != "do_b" {
		t.Fatalf("an action step keeps its action's preconditions, not the steps before it: %+v", second)
	}
	other, _ := c.Action("flow/phase/other:do_a")
	if !other.Pre[StepCondition("flow/manual")] || !other.Effects["c"] || !other.Effects["a"] || other.Declared() != "do_a" {
		t.Fatalf("a step may wait for a later step; each alternative reaches the step's criteria: %+v", other)
	}
	agentic, _ := c.Action("flow/agentic")
	if agentic.Builtin != BuiltinStep || agentic.Params["capability"] != "working" || len(agentic.Pre) != 1 || !agentic.Pre["b"] || !agentic.Effects["c"] {
		t.Fatalf("a capability step is entered by its own conditions and done by the goal of its method's agent: %+v", agentic)
	}
	manual, _ := c.Action("flow/manual")
	if manual.Kind != KindHuman || manual.Instructions != "Sign off" || !manual.Effects[StepCondition("flow/manual")] || len(manual.Pre) != 0 {
		t.Fatalf("a manual step is a human task, done once submitted: %+v", manual)
	}
	if info, ok := c.StepByPath("flow/manual"); !ok || len(info.References) != 1 || info.References[0].String() != "Checklist (doc:CHK-1), 2" || info.Planned[0] != "flow/manual" {
		t.Fatalf("the compiled step keeps its references: %+v", info)
	}
	if info, _ := c.StepByPath("flow/phase"); len(info.Steps) != 3 || !info.Exit["b"] || info.Entry["c"] {
		t.Fatalf("a phase has its sub-steps, entry and exit: %+v", info)
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
    steps: [{name: s, action: do_a, method: working}]`, "one thing"},
		"unknown action": {`
processes:
  - name: x
    steps: [{name: s, action: nope}]`, `unknown action "nope"`},
		"unknown step condition": {`
processes:
  - name: x
    steps: [{name: s, action: do_a, pre: {"step:x/nope": true}}]`, `unknown condition "step:x/nope"`},
		"undeclared role": {`
roles: [{name: dev}]
processes:
  - name: x
    steps: [{name: s, roles: {responsible: dev, accountable: boss}}]`, `unknown role "boss"`},
		"reference without document": {`
processes:
  - name: x
    steps: [{name: s, references: [{title: Guide}]}]`, "names its document"},
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
    steps: [{name: t, action: do_b, pre: {a: false}}]`, "both true and false"},
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
    steps: [{name: one}, {name: two, process: other/flow, pre: {"step:checklist/one": true}}]
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
	if !run.Effects["a"] || !run.Effects[StepCondition("inner/sign")] || len(run.Pre) != 0 {
		t.Fatalf("a nested process of the methodology is done by its exit criteria and needs nothing it makes itself: %+v", run)
	}
	// what a nested process needs from outside is the entry of the step nesting it
	c2, issues := compileProcess(t, `
processes:
  - name: outer
    steps: [{name: run, process: needy}]
  - name: needy
    steps:
      - {name: b_step, action: do_b}
      - {name: c_step, action: do_c, pre: {b: true}}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if r, _ := c2.Action("outer/run"); len(r.Pre) != 1 || !r.Pre["a"] {
		t.Fatalf("prerequisites of needy: a (do_b needs it, nothing in needy makes it; b is made by b_step): %+v", r.Pre)
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

func TestMethodsProvideACapabilityPerContext(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - name: quick
    for: finishing
    agent: worker
    priority: 5
    when: 'artifacts.exists(x, x.type == "urgent")'
    guidance: Do it quickly
    references: [{ref: "doc:QUICK"}]
  - {name: thorough, for: finishing, agent: worker, description: Do it well}
processes:
  - name: flow
    steps:
      - {name: finish, method: finishing, pre: {a: true}}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	a, _ := c.Action("flow/finish")
	if a.Builtin != BuiltinStep || a.Params["capability"] != "finishing" || !a.Effects["c"] || !a.Pre["a"] {
		t.Fatalf("a method step runs the chosen method, done by what the methods' goals share: %+v", a)
	}
	var bb domain.Blackboard
	if got := c.MethodsFor("finishing", bb); len(got) != 1 || got[0].Name != "thorough" || got[0].AgentGoal != "all" {
		t.Fatalf("outside its context a method does not apply: %+v", got)
	}
	bb.Change.Items = []domain.ChangeItem{{ID: "i1", Kind: domain.KindArtifact, Type: "urgent"}}
	if got := c.MethodsFor("finishing", bb); len(got) != 2 || got[0].Name != "quick" {
		t.Fatalf("the highest priority applicable method comes first: %+v", got)
	}
	if info, _ := c.StepByPath("flow/finish"); info.Method() != MethodCapability || info.Capability != "finishing" {
		t.Fatalf("step %+v", info)
	}
}

func TestMethodValidation(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"no actor": {`
methods: [{name: m, for: f}]
processes: [{name: x, steps: [{name: s, method: f}]}]`, "names its actor"},
		"unknown capability": {`
methods: [{name: m, for: f, agent: worker}]
processes: [{name: x, steps: [{name: s, method: g}]}]`, `no valid method provides "g"`},
		"bad context": {`
methods: [{name: m, for: f, agent: worker, when: "nope("}]
processes: [{name: x, steps: [{name: s, method: f}]}]`, "methods[0].when"},
		"unknown goal": {`
methods: [{name: m, for: f, agent: worker, goal: nope}]
processes: [{name: x, steps: [{name: s, method: f}]}]`, `"nope" is not a goal of agent worker`},
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

func TestProcessGraphDrawsTheConditions(t *testing.T) {
	m, err := LoadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g, ok := c.ProcessGraph("software_delivery")
	if !ok {
		t.Fatal("no graph")
	}
	edges := map[string]string{}
	for _, e := range g.Edges {
		edges[e.From+" -> "+e.To] = strings.Join(e.Conditions, ",")
	}
	for from, to := range map[string]string{
		"software_delivery/framing":                             "software_delivery/analysis/scope",
		"software_delivery/analysis/scope":                      "software_delivery/analysis/impacts",
		"software_delivery/analysis/specification/traceability": "software_delivery/design",
		"software_delivery/release":                             "software_delivery/application",
	} {
		if _, ok := edges[from+" -> "+to]; !ok {
			t.Errorf("no edge %s -> %s in %v", from, to, edges)
		}
	}
	// transitive reduction: framing does not point at the application directly
	if _, ok := edges["software_delivery/framing -> software_delivery/application"]; ok {
		t.Error("implied edge kept")
	}
	if len(g.Methods) != 2 || g.Methods[0].AgentGoal != "solution_design" {
		t.Fatalf("methods of the design capability: %+v", g.Methods)
	}
	var architect *GraphAgent
	for i := range g.Agents {
		if g.Agents[i].Name == "architect" {
			architect = &g.Agents[i]
		}
	}
	if architect == nil || len(architect.Actions) == 0 {
		t.Fatalf("the agents of the methods, with their actions: %+v", g.Agents)
	}
}
