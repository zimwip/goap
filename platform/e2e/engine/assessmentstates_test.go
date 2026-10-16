package enginee2e

import (
	"context"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
)

// The states of the assessments follow what the methodology declares, bound to its conditions (ADR 0116).
func TestAssessmentStatesFollowTheMethodology(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(describedYAML + `
assessments:
  - subject: description
    states:
      - {conditions: {description_accepted: true}, tone: ok, label: "good to go"}
      - {when: "assessment.score <= 1", tone: danger, label: "to be rewritten", open: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["described"] = c
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "described", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	rate := func(score int) condition.AssessmentState {
		t.Helper()
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		text, _ := assess.Text(bb, assess.SubjectDescription)
		if _, err := e.Graph.AddItems(ctx, ch.ID, []domain.ChangeItem{{Type: assess.TypeAssessment, Data: map[string]any{"key": "description", "score": score, "rated": text}}}); err != nil {
			t.Fatal(err)
		}
		states, err := e.AssessmentStates(ctx, ch.ID)
		if err != nil || len(states) != 1 {
			t.Fatalf("states %+v, %v", states, err)
		}
		return states[0]
	}
	if s := rate(4); s.Tone != condition.ToneOK || s.Label != "good to go" {
		t.Fatalf("accepted %+v", s)
	}
	if s := rate(1); s.Tone != condition.ToneDanger || !s.Open {
		t.Fatalf("poor %+v", s)
	}
	if s := rate(2); s.Tone != condition.ToneWarn || s.Label != "to be improved" {
		t.Fatalf("the defaults settle the rest %+v", s)
	}
}
