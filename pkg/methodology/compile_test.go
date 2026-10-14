package methodology

import (
	"strings"
	"testing"
)

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

func TestLoopIsChecked(t *testing.T) {
	m := &Methodology{Name: "m", Version: "1", Namespace: "alm", Actions: []Action{
		{Name: "code", Kind: KindLLM, Prompt: "x", Effects: map[string]bool{"done": true}, Loop: &Loop{MaxRounds: 200, Timeout: "2h"}},
		{Name: "bad", Kind: KindLLM, Prompt: "x", Effects: map[string]bool{"bad": true}, Loop: &Loop{MaxRounds: MaxLoopRounds + 1, Timeout: "soon"}},
		{Name: "tool", Kind: KindTool, Tool: "workspace/bash", Effects: map[string]bool{"built": true}, Loop: &Loop{MaxRounds: 3}},
	}, Goals: []Goal{{Name: "g", Pre: map[string]bool{"done": true}}}}
	_, issues := m.CompileLenient()
	var got []string
	for _, is := range issues {
		if strings.HasSuffix(is.Path, ".loop") {
			got = append(got, is.Path+": "+is.Message)
		}
	}
	want := []string{"actions[1].loop: loop.maxRounds must be between 0 and 500", "actions[1].loop: loop.timeout \"soon\" is not a duration (45m, 2h)",
		"actions[2].loop: loop is the budget of the tool loop of an llm action"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("issues:\n%s", strings.Join(got, "\n"))
	}
	if (&Loop{MaxRounds: 200}).Rounds() != 200 || (*Loop)(nil).Rounds() != DefaultToolRounds || (&Loop{Timeout: "9h"}).Duration() != MaxLoopTimeout {
		t.Fatal("bounds")
	}
}

// Two scoped actions of the same scope chain in a plan (ADR 0118): the first plans to leave a subject pending for
// the second, without checking it once it ran.
func TestScopedActionsChain(t *testing.T) {
	m, err := Parse([]byte(`
name: chain
version: 1.0.0
namespace: alm
conditions:
  - {name: written, scope: impact, expr: 'artifacts.exists(a, a.type == "w" && a.data.impact == self.key)'}
  - {name: checked, scope: impact, expr: 'artifacts.exists(a, a.type == "c" && a.data.impact == self.key)'}
  - {name: all_written, all: written}
  - {name: all_checked, all: checked}
actions:
  - {name: write, kind: human, scope: impact, effects: {written: true}}
  - {name: check, kind: human, scope: impact, pre: {written: true}, effects: {checked: true}}
goals:
  - {name: done, pre: {all_checked: true}}
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	w, _ := c.Action("write")
	if !w.Effects["pending:check"] || !w.Lifted["pending:check"] || !w.Effects["all_written"] {
		t.Fatalf("write %+v", w.Effects)
	}
	if ch, _ := c.Action("check"); ch.Effects["pending:write"] {
		t.Fatalf("check %+v", ch.Effects)
	}
}
