package methodology

import (
	"strings"
	"testing"
)

func testDomain() *Domain {
	return &Domain{Name: "alm", Version: "1", Schema: Schema{
		NodeTypes: []NodeType{{Name: "Requirement"}, {Name: "TestCase"}},
		LinkTypes: []LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
}

func refMethodology() *Methodology {
	return &Methodology{
		Name: "m", Version: "1", DomainRef: "alm@1",
		Conditions: []Condition{{Name: "c", Expr: `proposals.exists(l, l.op == "add_link" && l.link.type == "verifies")`}},
		Actions:    []Action{{Name: "a", Kind: KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

func resolver(d *Domain) DomainResolver {
	return func(name, version string) (*Domain, error) { return d, nil }
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

func TestResolveReferencedDomain(t *testing.T) {
	m, issues := refMethodology().Resolve(resolver(testDomain()))
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if issues := m.Validate(); len(issues) > 0 {
		t.Fatal(issues)
	}
	if len(m.Domain.NodeTypes) != 2 {
		t.Fatalf("domain not resolved: %+v", m.Domain)
	}
}

func TestResolveRejectsEmbeddedAndReferenced(t *testing.T) {
	m := refMethodology()
	m.Domain = Schema{NodeTypes: []NodeType{{Name: "X"}}}
	if _, issues := m.Resolve(resolver(testDomain())); len(issues) != 1 || issues[0].Path != "domainRef" {
		t.Fatalf("issues: %v", issues)
	}
}

func TestReferenceLint(t *testing.T) {
	m := refMethodology()
	m.Conditions[0].Expr = `proposals.exists(l, l.link.type == "satisfies") && proposals.exists(p, "Component" in p.node.types)`
	m.Actions[0].Expects = nil
	res, issues := m.Resolve(resolver(testDomain()))
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	got := res.Validate().Error()
	for _, want := range []string{"unknown link type satisfies", "unknown node type Component"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
