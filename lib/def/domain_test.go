package def

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/form"
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

func TestNodeTypeForms(t *testing.T) {
	d := &Domain{Name: "x", Version: "1.0.0", Schema: Schema{
		NodeTypes: []NodeType{
			{Name: "Base", Attributes: []Attribute{{Name: "title"}}},
			{Name: "Item", Extends: "Base", Attributes: []Attribute{{Name: "size"}}, Form: &form.Form{Fields: []form.Field{
				{Name: "title", Widget: form.WidgetText},
				{Name: "size", Widget: form.WidgetNumber},
				{Name: "colour", Widget: form.WidgetText},
				{Name: "parts", Widget: form.WidgetLink, Constraints: map[string]any{"fromKey": "OTHER"}},
				{Name: "made", Widget: form.WidgetNewNode},
			}}},
		},
	}}
	got := d.Validate().Error()
	for _, want := range []string{
		"nodeTypes[1].form.fields[2].name: colour is not an attribute of Item",
		"nodeTypes[1].form.fields[3].constraints.fromKey: a link field of a creation form starts from the node",
		"nodeTypes[1].form.fields[4].widget: a creation form sets the node: no newNode field",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "title is not") || strings.Contains(got, "size is not") {
		t.Errorf("own and inherited attributes accepted: %s", got)
	}
}
