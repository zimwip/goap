package decision

import (
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

var t0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// replay builds a change from events and replays its decision points under the policy.
type replay struct {
	t *testing.T
	c domain.Change
}

func (r *replay) add(id string, e domain.DecisionEvent) {
	r.c.Items = append(r.c.Items, domain.ChangeItem{ID: domain.ItemID(id), Kind: domain.KindDecisionPoint, DecisionEvent: &e, CreatedAt: t0.Add(time.Duration(len(r.c.Items)) * time.Minute)})
}

// open opens a point with the values the policy resolves.
func (r *replay) open(id string, given map[string]any, e domain.DecisionEvent) {
	r.t.Helper()
	values, err := Policy{}.Open(given, t0)
	if err != nil {
		r.t.Fatal(err)
	}
	e.Op, e.Policy = domain.DecisionOpenOp, values
	r.add(id, e)
}

func (r *replay) point(id string, at time.Time) domain.DecisionPoint {
	r.t.Helper()
	d, ok := r.c.DecisionPoint(id, at, Policy{})
	if !ok {
		r.t.Fatal("no point")
	}
	return d
}

// The confidence / rounds policy end to end through the replay: the expectations are those the graph had before the
// policy moved out of it (ADR 0067).
func TestPolicyReplay(t *testing.T) {
	r := &replay{t: t}
	point := func() domain.DecisionPoint { return r.point("P", t0.Add(time.Hour)) }
	r.open("P", map[string]any{"threshold": 0.8, "maxRounds": 2}, domain.DecisionEvent{Question: "which PSP?", Options: []string{"a", "b"}})
	if d := point(); d.Status != domain.PointOpen || Decider(d) != DeciderAgent || Threshold(d) != 0.8 {
		t.Fatalf("opened: %+v", d)
	}
	// undecidable: two questions block the point
	r.add("R1", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeUndecidable, Justification: "costs unknown", Questions: []string{"fees of a?", "fees of b?"}})
	if d := point(); d.Status != domain.PointBlocked || d.OpenQuestions() != 2 || Rounds(d) != 1 || d.Questions[0].ID != "R1:1" {
		t.Fatalf("blocked: %+v", d)
	}
	r.add("A1", domain.DecisionEvent{Op: domain.DecisionAnswerOp, Point: "P", QuestionID: "R1:1", Answer: "1%"})
	r.add("A2", domain.DecisionEvent{Op: domain.DecisionAnswerOp, Point: "P", QuestionID: "R1:2", Answer: "2%"})
	if d := point(); d.Status != domain.PointOpen || d.OpenQuestions() != 0 {
		t.Fatalf("answered: %+v", d)
	}
	// an agent ruling below the threshold waits for a human; a refusal is a round
	r.add("R2", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeDecided, Option: "a", Confidence: 0.6})
	if d := point(); d.Status != domain.PointRatifying {
		t.Fatalf("ratifying: %+v", d)
	}
	r.add("X", domain.DecisionEvent{Op: domain.DecisionRatifyOp, Point: "P", Accept: false})
	d := point()
	if d.Status != domain.PointEscalated || Rounds(d) != 2 || !d.NeedsHuman() || d.Escalation != "2 rounds without a decision" {
		t.Fatalf("two rounds: escalated: %+v", d)
	}
	r.add("R3", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeDecided, Option: "a", Human: true, By: "ann"})
	if d := point(); d.Status != domain.PointDecided || d.Option != "a" || d.DecidedBy != "ann" {
		t.Fatalf("decided: %+v", d)
	}
	// a decided point does not move any more
	r.add("R4", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeDecided, Option: "b", Human: true})
	if d := point(); d.Option != "a" {
		t.Fatalf("moved: %+v", d)
	}
	// past the deadline, a point is escalated
	r.open("Q", map[string]any{"maxDuration": "30m"}, domain.DecisionEvent{Question: "when?"})
	if d := r.point("Q", t0.Add(time.Hour)); d.Status != domain.PointEscalated || d.Escalation != "past the deadline 2026-09-01T00:30:00Z" {
		t.Fatalf("deadline: %+v", d)
	}
	if d := r.point("Q", t0.Add(10*time.Minute)); d.Status != domain.PointOpen || d.NeedsHuman() {
		t.Fatalf("before the deadline: %+v", d)
	}
}

func TestPolicyThresholdAndRatification(t *testing.T) {
	r := &replay{t: t}
	r.open("P", nil, domain.DecisionEvent{Question: "go?"}) // the defaults: 0.7 and 3 rounds
	if d := r.point("P", t0); Threshold(d) != DefaultThreshold || MaxRounds(d) != DefaultRounds {
		t.Fatalf("defaults: %v", d.Policy)
	}
	// at the threshold an agent settles the point alone, below it a person ratifies
	r.add("R1", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeDecided, Option: "x", Confidence: 0.69})
	if d := r.point("P", t0); d.Status != domain.PointRatifying {
		t.Fatalf("below: %+v", d)
	}
	r.add("X", domain.DecisionEvent{Op: domain.DecisionRatifyOp, Point: "P", Accept: true, By: "ann"})
	d := r.point("P", t0)
	if d.Status != domain.PointDecided || d.Option != "x" || d.DecidedBy != "ann" || d.Ruling == nil || !d.Ruling.Human || Rounds(d) != 0 {
		t.Fatalf("ratified: %+v", d)
	}
	r.open("S", nil, domain.DecisionEvent{Question: "again?"})
	r.add("R2", domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "S", Outcome: domain.OutcomeDecided, Option: "y", Confidence: 0.7, By: "agent"})
	if d := r.point("S", t0); d.Status != domain.PointDecided || d.DecidedBy != "agent" {
		t.Fatalf("at the threshold: %+v", d)
	}
	// three failed rounds escalate under the default
	r.open("T", nil, domain.DecisionEvent{Question: "stuck?"})
	for _, id := range []string{"U1", "U2", "U3"} {
		r.add(id, domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "T", Outcome: domain.OutcomeUndecidable, Justification: "?"})
	}
	if d := r.point("T", t0); Rounds(d) != 3 || d.Escalation != "3 rounds without a decision" {
		t.Fatalf("rounds: %+v", d)
	}
}

func TestPolicyHumanDecider(t *testing.T) {
	r := &replay{t: t}
	r.open("P", map[string]any{"decider": "human"}, domain.DecisionEvent{Question: "go?"})
	// reserved to a person by design: not an escalation
	if d := r.point("P", t0); d.Status != domain.PointOpen || !d.NeedsHuman() || !d.HumanOnly || d.Escalation != "" || Decider(d) != DeciderHuman {
		t.Fatalf("human decider: %+v", d)
	}
}

func TestPolicyOpen(t *testing.T) {
	got, err := Policy{}.Open(map[string]any{"decider": "human", "threshold": 0.9, "maxRounds": 5, "maxDuration": "2h"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{KeyDecider: "human", KeyThreshold: 0.9, KeyMaxRounds: int64(5), KeyDeadline: t0.Add(2 * time.Hour).Format(time.RFC3339Nano)}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// a Go duration and the numbers a JSON round trip gives
	if got, err = (Policy{}).Open(map[string]any{"maxDuration": time.Hour, "maxRounds": float64(2)}, t0); err != nil || got[KeyMaxRounds] != int64(2) || got[KeyDeadline] == nil {
		t.Fatalf("typed values: %v %v", got, err)
	}
	for name, bad := range map[string]map[string]any{
		"decider":   {"decider": "robot"},
		"threshold": {"threshold": 1.5},
		"negative":  {"threshold": -0.1},
		"duration":  {"maxDuration": "tomorrow"},
		"unknown":   {"thresold": 0.5},
		"number":    {"maxRounds": "many"},
	} {
		if _, err := (Policy{}).Open(bad, t0); err == nil {
			t.Errorf("%s: accepted %v", name, bad)
		}
	}
	// an unlimited number of rounds
	r := &replay{t: t}
	r.open("P", map[string]any{"maxRounds": -1}, domain.DecisionEvent{Question: "x?"})
	for _, id := range []string{"U1", "U2", "U3", "U4"} {
		r.add(id, domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: "P", Outcome: domain.OutcomeUndecidable, Justification: "?"})
	}
	if d := r.point("P", t0); Rounds(d) != 4 || d.Escalation != "" {
		t.Fatalf("no limit: %+v", d)
	}
}
