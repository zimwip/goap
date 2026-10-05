package methodology

import "testing"

// The one pipeline: strict builds nothing with issues, lenient builds what stands, stored adds the namespace rule
// without changing the others.
func TestCompileWithOptions(t *testing.T) {
	m := &Methodology{Name: "m", Version: "1",
		Conditions: []Condition{{Name: "done", Expr: "false"}},
		Goals:      []Goal{{Name: "g", Pre: map[string]bool{"done": true}}},
		Actions: []Action{
			{Name: "work", Kind: KindHuman, Instructions: "x", Effects: map[string]bool{"done": true}},
			{Name: "broken", Kind: KindHuman, Instructions: "x", Pre: map[string]bool{"nope": true}, Effects: map[string]bool{"done": true}},
		},
	}
	if c, issues := m.CompileWith(CompileOptions{}); c != nil || len(issues) != 1 {
		t.Fatalf("strict: %v %v", c, issues)
	}
	if c, issues := m.CompileWith(CompileOptions{Lenient: true}); c == nil || len(issues) != 1 {
		t.Fatalf("lenient: %v %v", c, issues)
	}
	issues := m.ValidateStored()
	if len(issues) != 2 || issues[0].Path != "namespace" {
		t.Fatalf("stored: %v", issues)
	}
	if c, issues := m.CompileWith(CompileOptions{Lenient: true, Stored: true}); c == nil || len(issues) != 2 {
		t.Fatalf("lenient stored: %v %v", c, issues)
	}
	m.Namespace = "alm"
	m.Actions = m.Actions[:1]
	if c, issues := m.CompileWith(CompileOptions{Stored: true}); c == nil || len(issues) != 0 {
		t.Fatalf("valid: %v %v", c, issues)
	}
}
