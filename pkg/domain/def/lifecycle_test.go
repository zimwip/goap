package def

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
      - {name: draft, notLandable: true}
      - {name: approved}
      - {name: obsolete, final: true}
    transitions:
      - {name: approve, from: draft, to: approved, permission: "requirement:approve"}
      - {name: reopen, from: approved, to: draft}
      - {name: retire, from: approved, to: obsolete}
  - name: spec
    initial: draft
    states: [{name: draft, notLandable: true}, {name: released}]
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
	if l := d.LifecycleOf("FunctionalRequirement"); l == nil || l.Initial != "draft" || l.Landable("draft") || !l.Landable("approved") {
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
}

func TestLifecycleValidation(t *testing.T) {
	bad := map[string]string{
		"unknown initial":      `{initial: nope, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}`,
		"no landable state":    `{initial: a, states: [{name: a, notLandable: true}, {name: b, notLandable: true}], transitions: [{name: t, from: a, to: b}]}`,
		"unknown target":       `{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: zz}]}`,
		"trapped not landable": `{initial: a, states: [{name: a, notLandable: true}, {name: b}]}`,
		"leaves a final":       `{initial: a, states: [{name: a, notLandable: true}, {name: b, final: true}], transitions: [{name: t, from: a, to: b}, {name: u, from: b, to: a}]}`,
		"duplicate state":      `{initial: a, states: [{name: a, notLandable: true}, {name: a}], transitions: [{name: t, from: a, to: a}]}`,
		"final not landable":   `{initial: a, states: [{name: a}, {name: b, notLandable: true, final: true}], transitions: [{name: t, from: a, to: b}]}`,
		"duplicate transition": `{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}, {name: t, from: a, to: b}]}`,
		"bad guard":            `{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b, guard: "node.props.("}]}`,
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
	// a lifecycle that flags nothing is valid: every state is landable, the initial one included
	if d, err := ParseDomain([]byte("name: x\nversion: 1\nlifecycles:\n  - {name: l, initial: a, states: [{name: a}, {name: b}], transitions: [{name: t, from: a, to: b}]}\nnodeTypes:\n  - {name: T, lifecycle: l}\n")); err != nil {
		t.Fatal(err)
	} else if is := d.Validate(); len(is) > 0 {
		t.Fatalf("all states landable: %v", is)
	}
	// references and documents
	const ok = "{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}"
	base := "name: x\nversion: 1\nlifecycles:\n  - {name: c, " + strings.TrimPrefix(ok, "{") + "\n  - {name: d, initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b, children: {states: [zz]}}]}\nnodeTypes:\n  - {name: C, lifecycle: c}\n"
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

// The vetos and objectives of a gate compile like a guard; each needs a name (unique in its list) and an expression (ADR 0075 §3).
func TestGateCriteriaIssues(t *testing.T) {
	const src = `
name: g
version: 1.0.0
lifecycles:
  - name: maturity
    initial: a
    states: [{name: a, notLandable: true}, {name: b}]
    transitions:
      - name: go
        from: a
        to: b
        vetos: [{name: safe, expr: 'world["safe"]'}, %s]
        objectives: [{name: docs, expr: 'world["docs"]'}, %s]
nodeTypes:
  - {name: Note}
`
	issues := func(veto, objective string) []Issue {
		d, err := ParseDomain([]byte(strings.Replace(strings.Replace(src, "%s", veto, 1), "%s", objective, 1)))
		if err != nil {
			t.Fatal(err)
		}
		return d.Validate()
	}
	if is := issues(`{name: other, expr: "true"}`, `{name: more, expr: "true"}`); len(is) > 0 {
		t.Fatalf("valid: %v", is)
	}
	for name, tc := range map[string][2]string{
		"unnamed veto":        {`{expr: "true"}`, `{name: more, expr: "true"}`},
		"duplicate veto":      {`{name: safe, expr: "true"}`, `{name: more, expr: "true"}`},
		"no expression":       {`{name: other}`, `{name: more, expr: "true"}`},
		"bad objective":       {`{name: other, expr: "true"}`, `{name: more, expr: "1 +"}`},
		"unknown variable":    {`{name: other, expr: "nope"}`, `{name: more, expr: "true"}`},
		"objective duplicate": {`{name: other, expr: "true"}`, `{name: docs, expr: "true"}`},
	} {
		if is := issues(tc[0], tc[1]); len(is) != 1 {
			t.Errorf("%s: issues %v", name, is)
		}
	}
}
