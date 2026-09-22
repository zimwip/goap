package methodology

import (
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/goap"
)

func load(t *testing.T) *Compiled {
	t.Helper()
	data, err := os.ReadFile("../../methodologies/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
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
