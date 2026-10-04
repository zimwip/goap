package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/condition"
)

func testDomain() *Domain {
	return &Domain{Name: "alm", Version: "1", Schema: Schema{
		NodeTypes: []NodeType{{Name: "Requirement"}, {Name: "TestCase"}},
		LinkTypes: []LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

func refMethodology() *Methodology {
	return &Methodology{
		Name: "m", Version: "1", Namespace: "alm",
		Conditions: []Condition{{Name: "c", Expr: `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@verifies"))`}},
		Actions:    []Action{{Name: "a", Kind: KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

// A link type may declare what it carries (architecture plan "Activity concept": specializes declares
// when/priority), documentary only - not yet enforced by the type catalogue, same as node property lists are
// for search/display rather than a hard schema.
func TestLinkTypeAttributesParse(t *testing.T) {
	d, err := ParseDomain([]byte(`
name: m
version: "1"
nodeTypes: [{name: A}]
linkTypes:
  - {name: specializes, from: A, to: A, attributes: [when, priority]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.LinkTypes) != 1 || len(d.LinkTypes[0].Attributes) != 2 || d.LinkTypes[0].Attributes[1].Name != "priority" {
		t.Fatalf("link type attributes: %+v", d.LinkTypes)
	}
}

func TestDomainValidate(t *testing.T) {
	if issues := testDomain().Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	bad := testDomain()
	bad.LinkTypes = append(bad.LinkTypes, LinkType{Name: "x", From: "Nope"})
	if issues := bad.Validate(); len(issues) != 1 || issues[0].Path != "linkTypes[1].from" {
		t.Fatalf("issues: %v", issues)
	}
}

func TestDomainYAMLRoundTrip(t *testing.T) {
	out, err := testDomain().YAML()
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDomain(out)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "alm" || len(d.NodeTypes) != 2 || d.LinkTypes[0].To != "Requirement" {
		t.Fatalf("round trip: %+v", d)
	}
}

func TestResolveTypes(t *testing.T) {
	m := refMethodology().Resolve(DomainTypes(testDomain()))
	if issues := m.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	if issues := refMethodology().ValidateStored(); len(issues) > 0 {
		t.Fatal(issues)
	}
	m.Namespace = ""
	if issues := m.ValidateStored(); len(issues) != 1 || issues[0].Path != "namespace" {
		t.Fatalf("a stored methodology names its target namespace: %v", issues)
	}
}

func TestReferenceLint(t *testing.T) {
	m := refMethodology()
	m.Conditions[0].Expr = `changeImpacts.exists(n, n.hasPost && n.post.out.exists(l, l.type == "alm@satisfies")) && changeImpacts.exists(n, "alm@Component" in n.types) && changeImpacts.exists(n, n.type == "Requirement")`
	m.Actions[0].Expects = &condition.Expectation{ForEach: "changeImpacts", Produce: condition.ProduceSpec{Op: "create_node", NodeType: "crm@Customer"}}
	got := m.Resolve(DomainTypes(testDomain())).Validate().Error()
	for _, want := range []string{"unknown link type alm@satisfies", "unknown node type alm@Component", `"Requirement" must be qualified`, "unknown node type crm@Customer"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	m.Actions[0].Expects.Produce.NodeType = "organisation@OrgUnit" // built in
	if !strings.Contains(m.Resolve(DomainTypes(testDomain())).Validate().Error(), "a methodology creates nodes of its namespace") {
		t.Error("a methodology creates nodes of its target namespace only")
	}
}

func TestNodeTypeEditor(t *testing.T) {
	d := testDomain()
	d.NodeTypes[0].Editor = "requirement-board"
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	out, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseDomain(out)
	if err != nil || back.NodeTypes[0].Editor != "requirement-board" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	d.NodeTypes[1].Editor = "Test Case"
	if issues := d.Validate(); len(issues) != 1 || issues[0].Path != "nodeTypes[1].editor" {
		t.Fatalf("issues: %v", issues)
	}
}
