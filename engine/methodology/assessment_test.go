package methodology

import (
	"strings"
	"testing"
)

func TestAssessmentViewsCompile(t *testing.T) {
	m := &Methodology{Name: "m", Namespace: "alm",
		Conditions: []Condition{{Name: "ready", Expr: "true"}},
		Goals:      []Goal{{Name: "g", Pre: map[string]bool{"ready": true}}},
		Actions:    []Action{{Name: "a", Kind: "human", Effects: map[string]bool{"ready": true}}},
		Assessments: []AssessmentView{{Subject: "description", States: []AssessmentState{
			{Conditions: map[string]bool{"ready": true}, Tone: "ok"},
			{Conditions: map[string]bool{"unknown": true}, Tone: "blue"},
		}}}}
	_, issues := m.CompileLenient()
	var got []string
	for _, is := range issues {
		got = append(got, is.Path)
	}
	all := strings.Join(got, " ")
	for _, want := range []string{"assessments[0].states[1].conditions.unknown", "assessments[0].states[1].tone"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in %v", want, issues)
		}
	}
	m.Assessments[0].States = m.Assessments[0].States[:1]
	if _, err := m.Compile(); err != nil {
		t.Fatal(err)
	}
}
