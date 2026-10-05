package def

import (
	"testing"
)

func testDomain() *Domain {
	return &Domain{Name: "alm", Version: "1", Schema: Schema{
		NodeTypes: []NodeType{{Name: "Requirement"}, {Name: "TestCase"}},
		LinkTypes: []LinkType{{Name: "verifies", From: "TestCase", To: "Requirement"}},
	}}
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
