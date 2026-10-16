package methodology

import "testing"

// The risk management methodology is transverse (ADR 0036 §3): no namespace, it applies to sdlc.
func TestRiskManagementMethodology(t *testing.T) {
	m, err := LoadFile("../../methodologies/risk-management.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if issues := m.ValidateStored(); len(issues) > 0 {
		t.Fatal(issues)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g, ok := c.Goal("risk_management")
	if !ok || !g.Pre["risks_reviewed"] || !g.Pre["risks_under_control"] || len(m.AppliesTo) != 1 || m.AppliesTo[0] != "sdlc" {
		t.Fatalf("goal %+v, appliesTo %v", g, m.AppliesTo)
	}
}
