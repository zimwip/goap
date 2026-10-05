package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The options and the decision points of a change in conditions, and the platform conditions (ADR 0009 §4).
func TestOptionsAndDecisionsInConditions(t *testing.T) {
	defs := append([]Definition{
		{Name: "all_evaluated", Expr: `options.all(o, o.status == "evaluated")`},
		{Name: "on_b", Expr: `activeOption == "b" && options.exists(o, o.active && o.name == "adyen")`},
		{Name: "fees_asked", Expr: `questions.exists(q, q.text.contains("fees") && q.status == "open")`},
		{Name: "two_rounds_left", Expr: `decisionPoints.all(d, d.maxRounds - d.rounds == 2)`},
	}, MustLibrary(LibraryDecisions)...)
	s, err := Compile(defs)
	if err != nil {
		t.Fatal(err)
	}
	bb := domain.Blackboard{
		Options: []domain.Flow{
			{ID: "a", Status: domain.FlowOpen, Option: &domain.OptionSpec{Name: "stripe"}, Evaluated: true},
			{ID: "b", Status: domain.FlowOpen, Option: &domain.OptionSpec{Name: "adyen"}, Evaluated: true, Active: true},
		},
		ActiveOption: "b",
		DecisionPoints: []domain.DecisionPoint{{ID: "p", Question: "which?", Status: domain.PointBlocked, Rounds: 1, MaxRounds: 3,
			Questions: []domain.Question{{ID: "p:1", Point: "p", Text: "What are the fees?", Status: domain.QuestionOpen}}}},
	}
	st := s.Evaluate(bb)
	if len(st.Errors) > 0 {
		t.Fatal(st.Errors)
	}
	want := map[string]bool{"all_evaluated": true, "on_b": true, "fees_asked": true, "two_rounds_left": true,
		"open_questions": true, "no_open_questions": false, "decision_ready": false, "decision_pending": true, "no_decision_pending": false,
		"options_open": true, "options_evaluated": true, "option_selected": false}
	for k, v := range want {
		if st.State[k] != v {
			t.Errorf("%s = %v, want %v", k, st.State[k], v)
		}
	}
	// without options nor decisions: no pending decision, no open question
	st = s.Evaluate(domain.Blackboard{})
	if !st.State["no_decision_pending"] || !st.State["no_open_questions"] || st.State["options_evaluated"] {
		t.Fatalf("empty: %v %v", st.State, st.Errors)
	}
}
