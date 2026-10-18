package methodology

import "testing"

// The proof of concept (ADR 0131): its process starts by itself on its sub-change and ends with a person's
// confirmation, which raises poc.concluded for the change that opened it.
func TestPOCMethodology(t *testing.T) {
	m, err := LoadFile("../../methodologies/poc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// a warning only: an empty POC waits for the scope its parent writes when it opens it
	if issues := m.ValidateStored(); issues.HasErrors() {
		t.Fatal(issues)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g, ok := c.Goal("conclude")
	if !ok || !g.Pre["conclusion_confirmed"] || m.Criticality != "C1" {
		t.Fatalf("goal %+v, criticality %s", g, m.Criticality)
	}
	a, ok := c.Action("confirm_conclusion")
	if !ok || len(a.Emits) != 1 || a.Emits[0] != "poc.concluded" {
		t.Fatalf("confirm_conclusion %+v", a)
	}
	if rs := c.Reactions(); len(rs) != 1 || rs[0].Activity != "proof_of_concept" {
		t.Fatalf("reactions %+v", rs)
	}
}
