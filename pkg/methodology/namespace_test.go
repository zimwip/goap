package methodology

import "testing"

func TestMethodologyImprovementTargetsTheMetadataNamespace(t *testing.T) {
	m, err := LoadFile("../../methodologies/methodology-improvement.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Namespace != "metadata" {
		t.Fatalf("namespace = %q", m.Namespace)
	}
}
