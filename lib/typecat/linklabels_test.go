package typecat

import "testing"

// A link type reads from its source with its label and from its target with its inverse label; the label falls back
// to the name, the inverse label to the label.
func TestLinkLabels(t *testing.T) {
	c, err := New(parse(t, `
name: lbl
version: 1.0.0
nodeTypes: [Req, Test]
linkTypes:
  - {name: verifies, from: Test, to: Req, label: verifies, inverseLabel: verified by}
  - {name: traces, from: Req, to: Req, label: traces to}
  - {name: relates, from: Req, to: Test}
`))
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string][2]string{
		"lbl@verifies": {"verifies", "verified by"},
		"lbl@traces":   {"traces to", "traces to"},
		"lbl@relates":  {"relates", "relates"},
	} {
		l, ok := c.LinkType(ref)
		if !ok {
			t.Fatalf("%s: unknown", ref)
		}
		if l.Label != want[0] || l.InverseLabel != want[1] {
			t.Errorf("%s: labels %q / %q, want %q / %q", ref, l.Label, l.InverseLabel, want[0], want[1])
		}
	}
}
