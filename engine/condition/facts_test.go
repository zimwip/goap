package condition

import (
	"testing"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
)

// The facts of a condition say which of its terms fails: a current rating of the description exists, its score is
// not enough; the rating action establishes the rating, nothing establishes the score.
func TestFactsOfACondition(t *testing.T) {
	assess.Register()
	defs, _ := Library("assessments")
	s, err := Compile(defs)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Change{ID: "C1", Title: "Note", Intent: "write a note"}
	bb := domain.Blackboard{Change: c}
	c.Items = []domain.ChangeItem{{ID: "i1", Type: assess.TypeAssessment, Status: domain.ItemAccepted,
		Data: map[string]any{"key": "description", "score": 2, "rated": assess.FingerprintOf(bb, assess.SubjectDescription)}}}
	bb.Change = c

	facts, ok := s.Facts("description_accepted", bb)
	if !ok || len(facts) != 1 || facts[0].Holds || facts[0].Over != "assessments" || len(facts[0].Parts) != 4 {
		t.Fatalf("facts %+v", facts)
	}
	parts := facts[0].Parts
	if !parts[0].Holds || !parts[1].Holds || !parts[2].Holds || parts[3].Holds || parts[3].Text != "a.score >= 3" {
		t.Fatalf("parts %+v", parts)
	}
	miss, _ := Missing(facts)
	if miss.Text != "a.score >= 3" || s.Establishes("description_rated", miss) {
		t.Fatalf("nothing but a rating scoring 3 makes the score: %+v", miss)
	}

	// before any rating, the rating itself is missing, and description_rated establishes it
	facts, _ = s.Facts("description_accepted", domain.Blackboard{Change: domain.Change{ID: "C1", Intent: "write a note"}})
	miss, _ = Missing(facts)
	if miss.Text != `a.key == "description"` || !s.Establishes("description_rated", miss) {
		t.Fatalf("a rating is missing first: %+v", miss)
	}
}
