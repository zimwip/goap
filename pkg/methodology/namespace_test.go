package methodology

import "testing"

func TestMethodologyImprovementTargetsTheMethodologyNamespace(t *testing.T) {
	m, err := LoadFile("../../methodologies/methodology-improvement.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Namespace != "methodology" {
		t.Fatalf("namespace = %q", m.Namespace)
	}
	if issues := m.ValidateStored(); len(issues) > 0 {
		t.Fatal(issues)
	}
}

func TestRepositoryMethodologiesAreStorable(t *testing.T) {
	for _, f := range []string{"../../methodologies/sdlc.yaml", "../../methodologies/examples/impact-analysis.yaml", "../../methodologies/examples/test-design.yaml"} {
		m, err := LoadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m.Namespace != "alm" {
			t.Fatalf("%s: namespace = %q", f, m.Namespace)
		}
		if issues := m.ValidateStored(); len(issues) > 0 {
			t.Fatalf("%s: %v", f, issues)
		}
	}
}
