package methodology

import (
	"strings"
	"testing"
)

// libraryYAML is a methodology whose goal needs risks_under_control, a condition of the risks library.
func libraryYAML(imports, extra string) string {
	return `
name: lib
version: 1.0.0
namespace: alm
` + imports + `
conditions:
  - {name: done, expr: 'artifacts.exists(a, a.type == "x")'}
` + extra + `
actions:
  - {name: do, kind: human, effects: {done: true}}
goals:
  - {name: g, pre: {done: true, risks_under_control: true}}
`
}

// The conditions of a library exist only in a methodology that imports it (ADR 0064).
func TestImportsAddOnlyTheLibrariesAsked(t *testing.T) {
	m, err := Parse([]byte(libraryYAML("", "")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Compile(); err == nil || !strings.Contains(err.Error(), "risks_under_control") {
		t.Fatalf("an unimported library condition must be unknown, got %v", err)
	}

	m, err = Parse([]byte(libraryYAML("imports: [risks]", "")))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Conditions.Has("risks_under_control") || c.Conditions.Has("open_questions") {
		t.Fatalf("conditions %v", c.Conditions.Names())
	}
}

func TestImportsRefuseAnUnknownLibrary(t *testing.T) {
	m, err := Parse([]byte(libraryYAML("imports: [risks, nope, risks]", "")))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, is := range m.Validate() {
		paths[is.Path] = true
	}
	if !paths["imports[1]"] || !paths["imports[2]"] {
		t.Fatalf("issues %v", m.Validate())
	}
}

// A condition the methodology declares replaces the one of the library.
func TestDeclaredConditionOverridesTheLibrary(t *testing.T) {
	m, err := Parse([]byte(libraryYAML("imports: [risks]", "  - {name: risks_under_control, expr: 'false'}")))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Conditions.Exprs()["risks_under_control"]; got != "false" {
		t.Fatalf("expr %q", got)
	}
}

func TestImportsYAMLRoundTrip(t *testing.T) {
	m, err := Parse([]byte(libraryYAML("imports: [risks, decisions]", "")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.YAML()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Imports) != 2 || m2.Imports[0] != "risks" || m2.Imports[1] != "decisions" {
		t.Fatalf("imports %v\n%s", m2.Imports, out)
	}
}
