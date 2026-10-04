package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
)

func domainIssues(t *testing.T, src string) string {
	t.Helper()
	d, err := ParseDomain([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, i := range d.Validate() {
		out = append(out, i.Path+": "+i.Message)
	}
	return strings.Join(out, "\n")
}

func TestAttributeShorthandAndValidators(t *testing.T) {
	d, err := ParseDomain([]byte(`
name: m
version: "1"
enums:
  - {name: prio, values: [low, {value: high, label: High}]}
nodeTypes:
  - name: A
    attributes: [title, {name: level, type: enum, enum: prio, validators: [v1]}]
    validators: [n1]
  - {name: B, extends: A, attributes: [{name: extra, validators: [v1]}]}
algorithms:
  - {name: pv, type: property_validator, language: javascript, code: "return"}
  - {name: nv, type: node_validator, language: javascript, code: "return"}
algorithmInstances:
  - {name: v1, algorithm: pv}
  - {name: n1, algorithm: nv}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.NodeTypes[0].Attributes; len(got) != 2 || got[0].Name != "title" || got[1].DefaultWidget() != WidgetDropdown {
		t.Fatalf("attributes: %+v", got)
	}
	if is := d.Validate(); len(is) > 0 {
		t.Fatalf("valid domain: %+v", is)
	}
	// the supertype's validators first: its attributes', its node validators, then the subtype's
	bs := d.BoundValidators("B")
	if len(bs) != 3 || bs[0].Property != "level" || bs[1].Type != algo.UsageNodeValidator || bs[2].Property != "extra" {
		t.Fatalf("bound validators: %+v", bs)
	}
}

func TestAttributeIssues(t *testing.T) {
	got := domainIssues(t, `
name: m
version: "1"
enums: [{name: e}, {name: e, values: [a, a]}]
nodeTypes:
  - name: A
    attributes:
      - {name: x, type: nope}
      - {name: x}
      - {name: y, widget: nope}
      - {name: z, type: enum}
      - {name: w, type: enum, enum: missing}
      - {name: v, enum: e}
      - {name: n1, asName: true}
      - {name: n2, asName: true}
      - {name: bad-name}
    validators: [ghost]
`)
	for _, want := range []string{
		"enums[0].values: an enum has at least one value", "enums[1].name: duplicate enum e", "enums[1].values[1].value: duplicate value a",
		"attributes[0].type: unknown type", "attributes[1].name: duplicate attribute x", "attributes[2].widget: unknown widget",
		"attributes[3].enum: an enum attribute names its enum", "attributes[4].enum: unknown enum missing", "attributes[5].enum: only an enum attribute",
		"attributes: at most one attribute is the name", "attributes[8].name: name must be", "validators[0]: unknown algorithm instance ghost",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing issue %q in:\n%s", want, got)
		}
	}
}
