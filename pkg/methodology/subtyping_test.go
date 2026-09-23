package methodology

import (
	"slices"
	"testing"
)

func TestSubtyping(t *testing.T) {
	m, err := Parse([]byte(`
name: st
version: "1.0.0"
domain:
  nodeTypes:
    - {name: Requirement}
    - {name: SecurityRequirement, extends: Requirement}
    - {name: CryptoRequirement, extends: SecurityRequirement}
conditions: [{name: c, expr: 'impacts.exists(i, "Requirement" in i.target.types)'}]
actions: [{name: a, kind: human, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if is := m.Validate(); len(is) > 0 {
		t.Fatal(is)
	}
	if got := m.Supertypes()["CryptoRequirement"]; !slices.Equal(got, []string{"SecurityRequirement", "Requirement"}) {
		t.Fatalf("supertypes %v", got)
	}
	m.Domain.NodeTypes[0].Extends = "CryptoRequirement"
	if is := m.Validate(); len(is) == 0 {
		t.Fatal("cycle not reported")
	}
	m.Domain.NodeTypes[0].Extends = "Nope"
	if is := m.Validate(); len(is) == 0 || is[0].Path != "domain.nodeTypes[0].extends" {
		t.Fatalf("unknown parent: %v", is)
	}
}
