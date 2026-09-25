package methodology

import (
	"strings"
	"testing"
)

const lifecycleDomain = `
name: docs
version: 1.0.0
lifecycles:
  - name: requirement
    initial: draft
    states:
      - {name: draft, editable: true}
      - {name: approved}
      - {name: obsolete, final: true}
    transitions:
      - {name: approve, from: draft, to: approved, permission: "requirement:approve"}
      - {name: reopen, from: approved, to: draft}
      - {name: retire, from: approved, to: obsolete}
  - name: spec
    initial: draft
    states: [{name: draft, editable: true}, {name: released}]
    transitions:
      - {name: release, from: draft, to: released, children: {states: [approved]}}
nodeTypes:
  - {name: Requirement, lifecycle: requirement}
  - {name: FunctionalRequirement, extends: Requirement}
  - {name: Spec, lifecycle: spec, document: {contains: [Requirement]}}
  - {name: Note, changeControlled: false}
linkTypes:
  - {name: contains, from: Spec, to: Requirement}
`

func TestLifecycleParsesAndInherits(t *testing.T) {
	d, err := ParseDomain([]byte(lifecycleDomain))
	if err != nil {
		t.Fatal(err)
	}
	if is := d.Validate(); len(is) > 0 {
		t.Fatalf("unexpected issues: %v", is)
	}
	if l := d.LifecycleOf("FunctionalRequirement"); l == nil || l.Initial != "draft" || !l.Editable("draft") || l.Editable("approved") {
		t.Fatalf("subtype must inherit the lifecycle: %+v", l)
	}
	if d.LifecycleOf("Note") != nil {
		t.Fatal("Note has no lifecycle")
	}
	if d.LifecycleOf("Requirement") != d.Lifecycle("requirement") || d.LifecycleOf("Spec").Name != "spec" {
		t.Fatal("a type resolves the lifecycle it names")
	}
	if d.NodeTypes[3].IsChangeControlled() || !d.NodeTypes[0].IsChangeControlled() {
		t.Fatal("changeControlled defaults to true")
	}
	// YAML round trip
	y, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ParseDomain(y)
	if err != nil || d2.LifecycleOf("Requirement") == nil || d2.NodeTypes[2].Document == nil || len(d2.Lifecycles) != 2 {
		t.Fatalf("round trip: %v %+v", err, d2)
	}
	// JSON meta round trip (structured store)
	var n NodeType
	n.SetMeta(d.NodeTypes[2].MetaJSON())
	if n.Lifecycle != "spec" || n.Document == nil {
		t.Fatalf("meta round trip: %+v", n)
	}
}

func TestLifecycleValidation(t *testing.T) {
	bad := map[string]string{
		"unknown initial":      `{initial: nope, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}`,
		"no editable state":    `{initial: a, states: [{name: a}, {name: b}]}`,
		"no fixed state":       `{initial: a, states: [{name: a, editable: true}]}`,
		"unknown target":       `{initial: a, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: zz}]}`,
		"trapped editable":     `{initial: a, states: [{name: a, editable: true}, {name: b}]}`,
		"leaves a final":       `{initial: a, states: [{name: a, editable: true}, {name: b, final: true}], transitions: [{name: t, from: a, to: b}, {name: u, from: b, to: a}]}`,
		"duplicate state":      `{initial: a, states: [{name: a, editable: true}, {name: a}], transitions: [{name: t, from: a, to: a}]}`,
		"final and editable":   `{initial: a, states: [{name: a, editable: true, final: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}`,
		"duplicate transition": `{initial: a, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: b}, {name: t, from: a, to: b}]}`,
		"bad guard":            `{initial: a, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: b, guard: "node.props.("}]}`,
	}
	for name, lc := range bad {
		src := "name: x\nversion: 1\nlifecycles:\n  - {name: l, " + strings.TrimPrefix(lc, "{") + "\nnodeTypes:\n  - {name: T, lifecycle: l}\n"
		d, err := ParseDomain([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if is := d.Validate(); len(is) == 0 {
			t.Errorf("%s: must be rejected", name)
		}
	}
	// references and documents
	const ok = "{initial: a, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}"
	base := "name: x\nversion: 1\nlifecycles:\n  - {name: c, " + strings.TrimPrefix(ok, "{") + "\n  - {name: d, initial: a, states: [{name: a, editable: true}, {name: b}], transitions: [{name: t, from: a, to: b, children: {states: [zz]}}]}\nnodeTypes:\n  - {name: C, lifecycle: c}\n"
	cases := map[string]string{
		"unknown lifecycle":    "  - {name: D, lifecycle: nope}\n",
		"unknown child type":   "  - {name: D, document: {contains: [Nope]}}\n",
		"children on non-doc":  "  - {name: D, lifecycle: d}\n",
		"unknown child state":  "  - {name: D, lifecycle: d, document: {contains: [C]}}\n",
		"lifecycle no control": "  - {name: D, lifecycle: c, changeControlled: false}\n",
	}
	for name, td := range cases {
		d, err := ParseDomain([]byte(base + td))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if is := d.Validate(); len(is) == 0 {
			t.Errorf("%s: must be rejected", name)
		}
	}
	dup, _ := ParseDomain([]byte("name: x\nversion: 1\nlifecycles:\n  - {name: c, " + strings.TrimPrefix(ok, "{") + "\n  - {name: c, " + strings.TrimPrefix(ok, "{") + "\nnodeTypes:\n  - {name: T}\n"))
	if is := dup.Validate(); len(is) == 0 {
		t.Error("duplicate lifecycle names must be rejected")
	}
}
