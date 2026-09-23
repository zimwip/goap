package methodology

import (
	"os"
	"testing"
)

func TestSDLCMethodologyIsValid(t *testing.T) {
	data, err := os.ReadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(cm.SpecializationsOf("sdlc", "build")); got != 4 {
		t.Fatalf("build specializations: %d", got)
	}
	if st := m.Supertypes()["SecurityRequirement"]; len(st) != 2 || st[1] != "Requirement" {
		t.Fatalf("supertypes: %v", st)
	}
}
