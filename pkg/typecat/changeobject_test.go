package typecat

import (
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain/def"
)

// The catalogue resolves the change object types of the domains (ADR 0098): a bare ref key is qualified, the enum
// values and the lifecycle are resolved, and the built-in execution types are always there.
func TestCatalogChangeObjectTypes(t *testing.T) {
	c, err := New(parse(t, `
name: risks
version: "1"
enums: [{name: level, values: [low, high]}]
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
    attributes: [{name: title, asName: true}, {name: level, type: enum, enum: level}]
  - {name: Mitigation, key: {kind: ref, ref: Risk}, attributes: [plan]}
  - {name: Review, key: {kind: ref, ref: impact}, scope: workspace}
`))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := c.ObjectType("risks@Risk")
	if !ok || r.Key.Kind != def.KeySequence || r.Key.Prefix != "RISK" || r.Scope != def.ScopeChange || r.Editor != "risk-register" || r.Lifecycle == nil || r.Lifecycle.Initial != "open" {
		t.Fatalf("risk: %+v", r)
	}
	if a, _ := attr(r.Attributes, "level"); len(a.Values) != 2 {
		t.Fatalf("enum values: %+v", r.Attributes)
	}
	if m, _ := c.ObjectType("risks@Mitigation"); m.Key.Ref != "risks@Risk" {
		t.Fatalf("a bare ref is qualified: %+v", m.Key)
	}
	if rv, _ := c.ObjectType("risks@Review"); rv.Key.Ref != def.RefImpact || rv.Scope != def.ScopeWorkspace {
		t.Fatalf("review: %+v", rv)
	}
	if checks := c.ObjectAttributeChecks("risks@Risk"); len(checks) != 2 || checks[1].Enum != "level" {
		t.Fatalf("attribute checks: %+v", checks)
	}
	if c.HasObjectType("risks@Nope") || c.HasNodeType("risks@Risk") || !c.HasObjectType("execution@Run") {
		t.Fatal("object types are their own kind of type")
	}
	var refs []string
	for _, o := range c.ObjectTypes() {
		refs = append(refs, o.Ref.String())
	}
	if len(refs) < 11 || refs[0] != "execution@Fact" {
		t.Fatalf("sorted object types: %v", refs)
	}
}

func TestCatalogChangeObjectTypeErrors(t *testing.T) {
	if _, err := New(parse(t, "name: x\nversion: \"1\"\nchangeObjectTypes: [{name: A, key: {kind: ref, ref: risks@Nope}}]\n")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("an unknown foreign ref: %v", err)
	}
	if _, err := New(parse(t, "name: x\nversion: \"1\"\nchangeObjectTypes: [{name: A, key: {kind: singleton}, lifecycle: nope}]\n")); !errors.Is(err, ErrUnknown) {
		t.Fatalf("an unknown lifecycle: %v", err)
	}
	if _, err := New(parse(t, "name: execution\nversion: \"9\"\nchangeObjectTypes: [{name: A, key: {kind: singleton}}]\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("execution is a built-in domain: %v", err)
	}
}

func attr(as []Attribute, name string) (Attribute, bool) {
	for _, a := range as {
		if a.Name == name {
			return a, true
		}
	}
	return Attribute{}, false
}
