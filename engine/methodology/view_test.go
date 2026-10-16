package methodology_test

import (
	"testing"

	"github.com/zimwip/goap/engine/methodology"
)

// The compiled view holds the activities as they are shown, and nothing of a change (ADR 0126 step 3).
func TestCompiledView(t *testing.T) {
	m := &methodology.Methodology{Name: "m", Version: "1", Namespace: "alm",
		Conditions: []methodology.Condition{{Name: "ready", Expr: "true"}, {Name: "done", Expr: "false", Description: "the work is done"}},
		Processes: []methodology.Process{{Name: "flow", Description: "the flow", Steps: []methodology.Step{
			{Name: "work", Action: "work", Pre: map[string]bool{"ready": true}},
		}}},
		Actions: []methodology.Action{{Name: "work", Kind: methodology.KindHuman, Instructions: "do it", Effects: map[string]bool{"done": true}}},
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if v != c.View() {
		t.Fatal("the view is computed again")
	}
	p, ok := v.Process("flow")
	if !ok || p.Kind != methodology.ActivityProcess || p.Agent == "" || p.Description != "the flow" || len(p.Done) == 0 {
		t.Fatalf("process %+v", p)
	}
	s := p.Steps[0]
	if s.Path != "flow/work" || s.Owner != "flow" || s.Method != methodology.MethodAction || s.Target != "work" || !s.Entry["ready"] || !s.Exit["done"] {
		t.Fatalf("step %+v", s)
	}
	if d := v.Condition("done"); d.Description != "the work is done" || len(d.ProducedBy) == 0 {
		t.Fatalf("condition done %+v", d)
	}
	for name := range p.Done {
		if v.Condition(name).Description == "" {
			t.Errorf("the generated condition %s of the goal carries no description", name)
		}
	}
}
