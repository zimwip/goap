package methodology

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/builtins"
)

const verifyMethodology = `
name: m
conditions: [{name: c, expr: "true"}]
actions:
  - {name: a, kind: human, instructions: x, effects: {c: true}, verify: {oracle: %s}}
  - {name: b, kind: human, instructions: x, effects: {c: true}, verify: {oracle: human, independent: false}}
goals: [{name: g, pre: {c: true}}]`

// verify parses from YAML, independent defaults to true when verify is set, and an unknown oracle is an issue (ADR 0075).
func TestActionVerify(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(verifyMethodology, "%s", "model", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if is := m.Validate(); len(is) > 0 {
		t.Fatalf("issues: %v", is)
	}
	a, b := m.Actions[0], m.Actions[1]
	if a.Verify == nil || a.Verify.Oracle != OracleModel || !a.Verify.IsIndependent() {
		t.Fatalf("a: %+v", a.Verify)
	}
	if b.Verify == nil || b.Verify.IsIndependent() {
		t.Fatalf("b: %+v", b.Verify)
	}
	if (*Verify)(nil).IsIndependent() {
		t.Fatal("no verify, no independence")
	}

	bad, err := Parse([]byte(strings.Replace(verifyMethodology, "%s", "oracle9", 1)))
	if err != nil {
		t.Fatal(err)
	}
	is := bad.Validate()
	if len(is) != 1 || is[0].Path != "actions[0].verify.oracle" || !strings.Contains(is[0].Message, `unknown oracle "oracle9"`) {
		t.Fatalf("issues: %v", is)
	}
	if _, err := bad.Compile(); err == nil {
		t.Fatal("compile must refuse an unknown oracle")
	}
}

// The example that applies the expiry of the derogations compiles and names a known builtin (ADR 0075 §2).
func TestDerogationExpiryExample(t *testing.T) {
	m, err := LoadFile("../../methodologies/examples/derogation-expiry.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m = m.WithBuiltins(builtins.Known{})
	if _, err := m.Compile(); err != nil {
		t.Fatal(err)
	}
}

// A methodology names a default criticality: C1, C2 or C3, anything else is an issue (ADR 0075 §3).
func TestMethodologyCriticality(t *testing.T) {
	const src = `
name: m
criticality: %s
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: human, instructions: x, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]`
	ok, err := Parse([]byte(strings.Replace(src, "%s", "C3", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if is := ok.Validate(); len(is) > 0 || ok.Criticality != "C3" {
		t.Fatalf("C3: %v %q", is, ok.Criticality)
	}
	bad, _ := Parse([]byte(strings.Replace(src, "%s", "C9", 1)))
	if is := bad.Validate(); len(is) != 1 || is[0].Path != "criticality" {
		t.Fatalf("issues: %v", is)
	}
	none, _ := Parse([]byte(strings.Replace(src, "criticality: %s\n", "", 1)))
	if is := none.Validate(); len(is) > 0 {
		t.Fatalf("none: %v", is)
	}
}
