package domain

import (
	"testing"
	"time"
)

func TestDecisionPointReplay(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c := Change{}
	add := func(id string, e DecisionEvent) {
		c.Items = append(c.Items, ChangeItem{ID: ItemID(id), Kind: KindDecisionPoint, DecisionEvent: &e, CreatedAt: t0.Add(time.Duration(len(c.Items)) * time.Minute)})
	}
	point := func() DecisionPoint {
		t.Helper()
		d, ok := c.DecisionPoint("P", t0.Add(time.Hour))
		if !ok {
			t.Fatal("no point")
		}
		return d
	}
	add("P", DecisionEvent{Op: DecisionOpenOp, Question: "which PSP?", Options: []string{"a", "b"}, Threshold: 0.8, MaxRounds: 2})
	if d := point(); d.Status != PointOpen || d.Decider != DeciderAgent {
		t.Fatalf("opened: %+v", d)
	}
	// undecidable: two questions block the point
	add("R1", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeUndecidable, Justification: "costs unknown", Questions: []string{"fees of a?", "fees of b?"}})
	if d := point(); d.Status != PointBlocked || d.OpenQuestions() != 2 || d.Rounds != 1 || d.Questions[0].ID != "R1:1" {
		t.Fatalf("blocked: %+v", d)
	}
	add("A1", DecisionEvent{Op: DecisionAnswerOp, Point: "P", QuestionID: "R1:1", Answer: "1%"})
	add("A2", DecisionEvent{Op: DecisionAnswerOp, Point: "P", QuestionID: "R1:2", Answer: "2%"})
	if d := point(); d.Status != PointOpen || d.OpenQuestions() != 0 {
		t.Fatalf("answered: %+v", d)
	}
	// an agent ruling below the threshold waits for a human; a refusal is a round
	add("R2", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "a", Confidence: 0.6})
	if d := point(); d.Status != PointRatifying {
		t.Fatalf("ratifying: %+v", d)
	}
	add("X", DecisionEvent{Op: DecisionRatifyOp, Point: "P", Accept: false})
	d := point()
	if d.Status != PointEscalated || d.Rounds != 2 || !d.NeedsHuman() {
		t.Fatalf("two rounds: escalated: %+v", d)
	}
	add("R3", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "a", Human: true, By: "ann"})
	if d := point(); d.Status != PointDecided || d.Option != "a" || d.DecidedBy != "ann" {
		t.Fatalf("decided: %+v", d)
	}
	// a decided point does not move any more
	add("R4", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "b", Human: true})
	if d := point(); d.Option != "a" {
		t.Fatalf("moved: %+v", d)
	}
	// past the deadline, a point is escalated
	add("Q", DecisionEvent{Op: DecisionOpenOp, Question: "when?", Deadline: t0.Add(30 * time.Minute)})
	if d, _ := c.DecisionPoint("Q", t0.Add(time.Hour)); d.Status != PointEscalated {
		t.Fatalf("deadline: %+v", d)
	}
	if d, _ := c.DecisionPoint("Q", t0.Add(10*time.Minute)); d.Status != PointOpen {
		t.Fatalf("before the deadline: %+v", d)
	}
}
