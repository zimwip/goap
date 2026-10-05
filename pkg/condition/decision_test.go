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
		{Name: "sure_enough", Expr: `decisionPoints.all(d, d.policy.threshold >= 0.5 && d.humanOnly == false)`},
		{Name: "two_rounds_left", Expr: `decisionPoints.all(d, d.policy.maxRounds - d.policy.rounds == 2)`},
	}, MustLibrary(LibraryDecisions)...)
	s, err := Compile(defs)
	if err != nil {
		t.Fatal(err)
	}
	bb := domain.Blackboard{Facets: map[string]any{
		domain.FacetOptions: []domain.Flow{
			{ID: "a", Status: domain.FlowOpen, Option: &domain.OptionSpec{Name: "stripe"}, Evaluated: true},
			{ID: "b", Status: domain.FlowOpen, Option: &domain.OptionSpec{Name: "adyen"}, Evaluated: true, Active: true},
		},
		domain.FacetActiveOption: "b",
		domain.FacetDecisionPoints: []domain.DecisionPoint{{ID: "p", Question: "which?", Status: domain.PointBlocked, Policy: map[string]any{"rounds": int64(1), "maxRounds": float64(3), "threshold": 0.7},
			Questions: []domain.Question{{ID: "p:1", Point: "p", Text: "What are the fees?", Status: domain.QuestionOpen}}}},
	}}
	st := s.Evaluate(bb)
	if len(st.Errors) > 0 {
		t.Fatal(st.Errors)
	}
	want := map[string]bool{"all_evaluated": true, "on_b": true, "fees_asked": true, "two_rounds_left": true, "sure_enough": true,
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
