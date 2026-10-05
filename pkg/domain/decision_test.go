package domain

import (
	"fmt"
	"testing"
	"time"
)

// The mechanism of a decision point under the default policy (a decided ruling of anyone settles it, nothing is
// reserved to a person): the policy-bearing behaviour is tested in pkg/decision.
func TestDecisionPointReplay(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c := Change{}
	add := func(id string, e DecisionEvent) {
		c.Items = append(c.Items, ChangeItem{ID: ItemID(id), Kind: KindDecisionPoint, DecisionEvent: &e, CreatedAt: t0.Add(time.Duration(len(c.Items)) * time.Minute)})
	}
	point := func() DecisionPoint {
		t.Helper()
		d, ok := c.DecisionPoint("P", t0.Add(time.Hour), nil)
		if !ok {
			t.Fatal("no point")
		}
		return d
	}
	add("P", DecisionEvent{Op: DecisionOpenOp, Question: "which PSP?", Options: []string{"a", "b"}})
	if d := point(); d.Status != PointOpen || d.NeedsHuman() || d.Escalation != "" {
		t.Fatalf("opened: %+v", d)
	}
	// undecidable: two questions block the point
	add("R1", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeUndecidable, Justification: "costs unknown", Questions: []string{"fees of a?", "fees of b?"}})
	if d := point(); d.Status != PointBlocked || d.OpenQuestions() != 2 || d.Questions[0].ID != "R1:1" {
		t.Fatalf("blocked: %+v", d)
	}
	add("A1", DecisionEvent{Op: DecisionAnswerOp, Point: "P", QuestionID: "R1:1", Answer: "1%"})
	add("A2", DecisionEvent{Op: DecisionAnswerOp, Point: "P", QuestionID: "R1:2", Answer: "2%"})
	if d := point(); d.Status != PointOpen || d.OpenQuestions() != 0 {
		t.Fatalf("answered: %+v", d)
	}
	// whoever rules decided settles the point, whatever the confidence
	add("R2", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "a", Confidence: 0.1, By: "agent"})
	if d := point(); d.Status != PointDecided || d.Option != "a" || d.DecidedBy != "agent" {
		t.Fatalf("decided: %+v", d)
	}
	// a decided point does not move any more
	add("R3", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "b", Human: true})
	add("X", DecisionEvent{Op: DecisionRatifyOp, Point: "P", Accept: true})
	if d := point(); d.Option != "a" || d.DecidedBy != "agent" {
		t.Fatalf("moved: %+v", d)
	}
	// no deadline, no rounds under the default policy
	add("Q", DecisionEvent{Op: DecisionOpenOp, Question: "when?"})
	for i := 0; i < 5; i++ {
		add(fmt.Sprintf("U%d", i), DecisionEvent{Op: DecisionRuleOp, Point: "Q", Outcome: OutcomeUndecidable, Justification: "?", Questions: []string{"q"}})
	}
	if d, _ := c.DecisionPoint("Q", t0.Add(1000*time.Hour), nil); d.Status != PointBlocked || d.Escalation != "" || d.NeedsHuman() {
		t.Fatalf("no rounds, no deadline: %+v", d)
	}
	// the default policy takes no policy values, the opener is told
	if _, err := DefaultDecisionPolicy().Open(map[string]any{"threshold": 0.5}, t0); err == nil {
		t.Fatal("the default policy accepted policy values")
	}
}

// reserving is a policy that holds every ruling for ratification, counts in the point and reserves the point to a
// person once it has two failed rounds or a deadline passed: the replay only does what the policy says.
type reserving struct{}

func (reserving) Open(given map[string]any, now time.Time) (map[string]any, error) { return given, nil }

func (reserving) Fold(p *DecisionPoint, ev DecisionEvent) bool {
	if p.Policy == nil {
		p.Policy = map[string]any{}
	}
	n, _ := p.Policy["seen"].(int)
	p.Policy["seen"] = n + 1
	return ev.Op == DecisionRatifyOp && ev.Accept
}

func (reserving) Reserve(p DecisionPoint, now time.Time) (bool, string) {
	if n, _ := p.Policy["seen"].(int); n >= 2 {
		return true, "seen twice"
	}
	return p.Policy["human"] == true, ""
}

func TestDecisionPointReplayAsksThePolicy(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c := Change{}
	add := func(id string, e DecisionEvent) {
		c.Items = append(c.Items, ChangeItem{ID: ItemID(id), Kind: KindDecisionPoint, DecisionEvent: &e, CreatedAt: t0.Add(time.Duration(len(c.Items)) * time.Minute)})
	}
	point := func(id string) DecisionPoint {
		t.Helper()
		d, ok := c.DecisionPoint(id, t0.Add(time.Hour), reserving{})
		if !ok {
			t.Fatal("no point")
		}
		return d
	}
	given := map[string]any{"human": true}
	add("P", DecisionEvent{Op: DecisionOpenOp, Question: "go?", Policy: given})
	if d := point("P"); d.Status != PointOpen || !d.HumanOnly || !d.NeedsHuman() || d.Escalation != "" || d.Policy["human"] != true {
		t.Fatalf("reserved by design: %+v", d)
	}
	add("R1", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "a", Confidence: 1})
	if d := point("P"); d.Status != PointRatifying || d.Ruling == nil || d.DecidedBy != "" {
		t.Fatalf("the policy did not settle the ruling: %+v", d)
	}
	add("X", DecisionEvent{Op: DecisionRatifyOp, Point: "P", Accept: false})
	d := point("P")
	if d.Status != PointEscalated || d.Escalation != "seen twice" || d.Ruling != nil {
		t.Fatalf("a refused ratification: %+v", d)
	}
	add("X2", DecisionEvent{Op: DecisionRuleOp, Point: "P", Outcome: OutcomeDecided, Option: "a", Human: true, By: "ann"})
	add("X3", DecisionEvent{Op: DecisionRatifyOp, Point: "P", Accept: true, By: "ann"})
	if d := point("P"); d.Status != PointDecided || d.Option != "a" || d.DecidedBy != "ann" || !d.Ruling.Human {
		t.Fatalf("ratified: %+v", d)
	}
	// the point holds its own copy of the values: the policy state never reaches the log
	if _, leaked := given["seen"]; leaked {
		t.Fatalf("the policy state leaked into the event: %v", given)
	}
	// the replay is a function of the log: the policy state does not leak from one replay to the next
	if a, b := point("P"), point("P"); a.Policy["seen"] != b.Policy["seen"] {
		t.Fatalf("replays differ: %v %v", a.Policy, b.Policy)
	}
}
