package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/goap"
)

func load(t *testing.T) *Compiled {
	t.Helper()
	m, err := LoadFile("../../methodologies/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExampleCompilesAndPlans(t *testing.T) {
	c := load(t)
	if !c.Conditions.Has("expect:propose_tests") {
		t.Fatal("expects must generate a condition")
	}
	start := goap.WorldState{}
	for _, n := range c.Conditions.Names() {
		start[n] = false
	}
	g, _ := c.Goal("prepare_change")
	p, err := goap.Planner{}.Plan(start, c.PlanningActions(), g.PlanningGoal())
	if err != nil {
		t.Fatal(err)
	}
	got := p.String()
	for _, want := range []string{"identify_impacts", "propagate_impacts", "propose_requirement_updates", "propose_tests", "review_proposals"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan %s misses %s", got, want)
		}
	}
	if strings.Contains(got, "select_impacts") {
		t.Errorf("cheaper identify_impacts should be preferred: %s", got)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"unknown condition": `
name: m
goals: [{name: g, pre: {nope: true}}]`,
		"no effect": `
name: m
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: builtin, builtin: x}]
goals: [{name: g, pre: {c: true}}]`,
		"bad kind": `
name: m
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: magic, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]`,
		"bad expr": `
name: m
conditions: [{name: c, expr: "impacts +"}]
goals: [{name: g, pre: {c: true}}]`,
		"unknown field": `
name: m
bogus: 1
goals: [{name: g, pre: {c: true}}]`,
	}
	for name, src := range cases {
		m, err := Parse([]byte(src))
		if err == nil {
			_, err = m.Compile()
		}
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestIssuesArePathed(t *testing.T) {
	m, err := Parse([]byte(`
name: Bad Name
conditions:
  - {name: ok, expr: "size(impacts) > 0"}
  - {name: broken, expr: "impacts +"}
actions:
  - {name: a, kind: llm, effects: {ok: true, ghost: true}}
goals:
  - {name: g, pre: {}}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, is := range m.Validate() {
		got[is.Path] = true
	}
	for _, want := range []string{"name", "conditions[1].expr", "actions[0].prompt", "actions[0].effects.ghost", "goals[0].pre"} {
		if !got[want] {
			t.Errorf("missing issue %s in %v", want, got)
		}
	}
}

func TestCompileDoesNotMutateAndYAMLRoundTrip(t *testing.T) {
	m, err := LoadFile("../../methodologies/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.Actions {
		if a.Effects[a.ExpectCondition()] {
			t.Fatalf("compile leaked generated effect into the definition: %s", a.Name)
		}
	}
	if a, _ := c.Action("propose_tests"); !a.Effects["expect:propose_tests"] {
		t.Fatal("compiled action must carry the generated effect")
	}
	out, err := m.YAML()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if _, err := m2.Compile(); err != nil {
		t.Fatal(err)
	}
	if len(m2.Actions) != len(m.Actions) || m2.Actions[2].Params["maxDepth"] != m.Actions[2].Params["maxDepth"] {
		t.Fatalf("round trip lost data")
	}
}
