package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/builtins"
)

type setOf map[string]bool

func (s setOf) HasBuiltin(name string) bool { return s[name] }

const builtinMethodology = `
name: m
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: builtin, builtin: nosuch, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]`

func TestUnknownBuiltin(t *testing.T) {
	m, err := Parse([]byte(builtinMethodology))
	if err != nil {
		t.Fatal(err)
	}
	// no set: the name is not checked
	if is := m.Validate(); len(is) > 0 {
		t.Fatalf("unchecked: %v", is)
	}
	// a set that does not know it: refused, and compiling refuses it too
	r := m.WithBuiltins(setOf{"other": true})
	is := r.Validate()
	if len(is) != 1 || is[0].Path != "actions[0].builtin" || !strings.Contains(is[0].Message, `unknown builtin "nosuch"`) {
		t.Fatalf("issues: %v", is)
	}
	if _, err := r.Compile(); err == nil {
		t.Fatal("compile must refuse an unknown builtin")
	}
	// registered: accepted
	if is := m.WithBuiltins(setOf{"nosuch": true}).Validate(); len(is) > 0 {
		t.Fatalf("registered: %v", is)
	}
	// the platform's static list knows its own names
	if !(builtins.Known{}).HasBuiltin(builtins.GraphApply) || (builtins.Known{}).HasBuiltin("nosuch") {
		t.Fatal("static set")
	}
}
