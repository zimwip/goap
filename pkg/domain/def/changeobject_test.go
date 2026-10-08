package def

import (
	"strings"
	"testing"
)

const risksDomain = `
name: risks
version: "1"
enums:
  - {name: level, values: [low, high]}
lifecycles:
  - name: risk
    initial: open
    states: [{name: open}, {name: closed, final: true}]
    transitions: [{name: close, from: open, to: closed}]
changeObjectTypes:
  - name: Risk
    key: {kind: sequence, prefix: RISK}
    lifecycle: risk
    editor: risk-register
    attributes:
      - {name: title, type: string, asName: true}
      - {name: level, type: enum, enum: level}
    search: [{property: title, text: true}]
  - name: Mitigation
    key: {kind: ref, ref: Risk}
    attributes: [plan]
  - name: Review
    key: {kind: ref, ref: impact}
    scope: workspace
  - name: Threshold
    key: {kind: singleton}
  - name: Owner
    key: {kind: natural, attributes: [role]}
    attributes: [role, who]
`

// A domain may declare change object types only (ADR 0098), each with its key type, attributes, lifecycle and editor.
func TestChangeObjectTypesParseAndValidate(t *testing.T) {
	d, err := ParseDomain([]byte(risksDomain))
	if err != nil {
		t.Fatal(err)
	}
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	r := d.ChangeObjectTypes[0]
	if r.Key.Kind != KeySequence || r.Key.Prefix != "RISK" || r.Lifecycle != "risk" || r.Editor != "risk-register" || r.ScopeOrDefault() != ScopeChange {
		t.Fatalf("risk: %+v", r)
	}
	if d.ChangeObjectTypes[2].ScopeOrDefault() != ScopeWorkspace {
		t.Fatalf("review scope: %+v", d.ChangeObjectTypes[2])
	}
	out, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseDomain(out)
	if err != nil || len(again.ChangeObjectTypes) != 5 || again.ChangeObjectTypes[4].Key.Attributes[0] != "role" {
		t.Fatalf("round trip: %v %+v", err, again)
	}
}

func TestChangeObjectTypeIssues(t *testing.T) {
	for _, tc := range []struct{ yaml, path, msg string }{
		{`[{name: A}]`, "changeObjectTypes[0].key.kind", "required"},
		{`[{name: A, key: {kind: serial}}]`, "changeObjectTypes[0].key.kind", "unknown key kind"},
		{`[{name: A, key: {kind: sequence}}]`, "changeObjectTypes[0].key.prefix", "prefix"},
		{`[{name: A, key: {kind: sequence, prefix: risk}}]`, "changeObjectTypes[0].key.prefix", "prefix"},
		{`[{name: A, key: {kind: natural}}]`, "changeObjectTypes[0].key.attributes", "names the attributes"},
		{`[{name: A, key: {kind: natural, attributes: [x]}}]`, "changeObjectTypes[0].key.attributes[0]", "unknown attribute x"},
		{`[{name: A, key: {kind: ref}}]`, "changeObjectTypes[0].key.ref", "names what it designates"},
		{`[{name: A, key: {kind: ref, ref: Nope}}]`, "changeObjectTypes[0].key.ref", "unknown target Nope"},
		{`[{name: A, key: {kind: singleton, prefix: X}}]`, "changeObjectTypes[0].key.prefix", "only a sequence key"},
		{`[{name: A, key: {kind: singleton, ref: run}}]`, "changeObjectTypes[0].key.ref", "only a ref key"},
		{`[{name: A, key: {kind: singleton}, scope: branch}]`, "changeObjectTypes[0].scope", "unknown scope"},
		{`[{name: A, key: {kind: singleton}, lifecycle: nope}]`, "changeObjectTypes[0].lifecycle", "unknown lifecycle"},
		{`[{name: A, key: {kind: singleton}, editor: Bad Editor}]`, "changeObjectTypes[0].editor", "invalid editor"},
		{`[{name: A, key: {kind: singleton}, search: [{property: x, text: true}]}]`, "changeObjectTypes[0].search[0].property", "unknown property"},
		{`[{name: A, key: {kind: singleton}, attributes: [{name: x, type: enum}]}]`, "changeObjectTypes[0].attributes[0].enum", "names its enum"},
		{`[{name: A, key: {kind: singleton}}, {name: A, key: {kind: singleton}}]`, "changeObjectTypes[1].name", "duplicate"},
		{`[{name: Requirement, key: {kind: singleton}}]`, "changeObjectTypes[0].name", "already a node or link type"},
	} {
		d, err := ParseDomain([]byte("name: x\nversion: \"1\"\nnodeTypes: [Requirement]\nchangeObjectTypes: " + tc.yaml + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", tc.yaml, err)
		}
		found := false
		for _, i := range d.Validate() {
			if i.Path == tc.path && strings.Contains(i.Message, tc.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s %q, got %v", tc.yaml, tc.path, tc.msg, d.Validate())
		}
	}
}

// A ref key may name a change object type of another domain, which the type catalogue resolves.
func TestChangeObjectTypeForeignRef(t *testing.T) {
	d, err := ParseDomain([]byte("name: x\nversion: \"1\"\nchangeObjectTypes: [{name: A, key: {kind: ref, ref: risks@Risk}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
}

// The built-in domain execution holds the engine's change object types (ADR 0098).
func TestBuiltinExecutionDomain(t *testing.T) {
	if !IsBuiltinDomain("execution") {
		t.Fatal("execution is a built-in domain")
	}
	for _, d := range BuiltinDomains() {
		if d.Name != "execution" {
			continue
		}
		names := map[string]string{}
		for _, ct := range d.ChangeObjectTypes {
			names[ct.Name] = ct.Key.Kind
		}
		for name, kind := range map[string]string{"Methodology": KeyNatural, "State": KeySingleton, "Transition": KeySequence, "Fact": KeySequence,
			"Option": KeyRef, "Run": KeyRef, "Journal": KeySequence, "ModelCall": KeySequence} {
			if names[name] != kind {
				t.Errorf("execution@%s: key %q, want %q", name, names[name], kind)
			}
		}
	}
}
