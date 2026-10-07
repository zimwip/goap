package methodology

import (
	"github.com/zimwip/goap/pkg/domain/def"
	"strings"
	"testing"
)

func TestHintsWarnWhenNothingCanStart(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string // "" : no hint
	}{
		"a first step": {`
name: ok
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}]
actions: [{name: x, kind: human, effects: {a: true}}]
processes: [{name: p, steps: [{name: first, action: x}]}]
`, ""},
		"every step waits": {`
name: stuck
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}, {name: b, expr: "false"}, {name: c, expr: "false"}]
actions:
  - {name: x, kind: human, pre: {c: true}, effects: {a: true}}
  - {name: y, kind: human, pre: {a: true}, effects: {b: true}}
processes: [{name: p, steps: [{name: one, action: x}, {name: two, action: y}]}]
`, "a, c"},
		"declared agent, no process": {`
name: plain
namespace: alm
goal: g
conditions: [{name: a, expr: "false"}, {name: b, expr: "false"}]
actions: [{name: x, kind: human, pre: {a: true}, effects: {b: true}}]
goals: [{name: g, pre: {b: true}}]
`, "first: a"},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := Parse([]byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			c, err := m.Compile()
			if err != nil {
				t.Fatal(err)
			}
			hints := c.Hints()
			if tc.want == "" && len(hints) != 0 {
				t.Fatalf("%v", hints)
			}
			if tc.want != "" && (len(hints) != 1 || hints[0].Path != "goal" || !strings.Contains(hints[0].Message, tc.want)) {
				t.Fatalf("%v", hints)
			}
		})
	}
}

// The shipped methodologies can start on an empty change.
func TestShippedMethodologiesHaveNoHint(t *testing.T) {
	for _, f := range []string{"sdlc", "methodology-improvement", "examples/impact-analysis", "examples/test-design", "examples/option-decision"} {
		m, err := LoadFile("../../methodologies/" + f + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		c, err := m.Compile()
		if err != nil {
			t.Fatal(err)
		}
		if h := c.Hints(); len(h) != 0 {
			t.Errorf("%s: %v", f, h)
		}
	}
}

const stuckSrc = `
name: stuck
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}, {name: c, expr: "false"}]
actions: [{name: x, kind: human, pre: {c: true}, effects: {a: true}}]
processes: [{name: p, steps: [{name: one, action: x}]}]
`

// A hint is a warning of the compile: lenient and strict builds carry it, nothing is refused for it.
func TestHintIsAWarningThatDoesNotBlock(t *testing.T) {
	m, err := Parse([]byte(stuckSrc))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil || c == nil {
		t.Fatalf("a warning must not refuse the compile: %v", err)
	}
	for name, issues := range map[string]def.Issues{"validate": m.Validate(), "stored": m.ValidateStored()} {
		if issues.HasErrors() || len(issues.Warnings()) != 1 || issues.Warnings()[0].Path != "goal" || !issues[0].IsWarning() {
			t.Errorf("%s: %v", name, issues)
		}
	}
	lc, issues := m.CompileLenient()
	if lc == nil || issues.HasErrors() || len(issues.Warnings()) != 1 {
		t.Fatalf("lenient: %v", issues)
	}
}

// With an error the hints are left out and Compile refuses with the errors only.
func TestErrorsRefuseAndKeepTheirSeverity(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(stuckSrc, "goal: p", "goal: nope", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Compile(); err == nil {
		t.Fatal("an unknown goal must refuse")
	}
	_, issues := m.CompileLenient()
	if !issues.HasErrors() || len(issues.Warnings()) != 0 || issues[0].IsWarning() || issues[0].Severity != "" {
		t.Fatalf("%v", issues)
	}
}

func TestShippedMethodologiesHaveNoWarning(t *testing.T) {
	for _, f := range []string{"sdlc", "methodology-improvement", "risk-management"} {
		m, err := LoadFile("../../methodologies/" + f + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if w := m.Validate().Warnings(); len(w) != 0 {
			t.Errorf("%s: %v", f, w)
		}
	}
}
