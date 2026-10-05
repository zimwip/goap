package dsl

import (
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Options and decision points of the change (ADR 0009 §3-4), as scripts see and work on them.

// Option is an option of the change: a hypothesis explored on a flow of its own.
type Option struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hypothesis string `json:"hypothesis"`
	// Status is exploring, evaluated, selected or rejected.
	Status     string `json:"status"`
	Active     bool   `json:"active"`
	Evaluation string `json:"evaluation"`
}

// Question is a question raised by an undecidable ruling.
type Question struct {
	ID     string `json:"id"`
	Point  string `json:"point"`
	Text   string `json:"text"`
	Status string `json:"status"` // open | answered
	Answer string `json:"answer"`
}

// DecisionPoint is a decision point of the change.
type DecisionPoint struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Criteria []string `json:"criteria"`
	// Policy holds the policy values of the point (who decides, thresholds, rounds: the keys of pkg/decision).
	Policy map[string]any `json:"policy"`
	// Status is open, blocked, ratifying, escalated or decided.
	Status    string     `json:"status"`
	Questions []Question `json:"questions"`
	// Option is the option decided (once decided).
	Option string `json:"option"`
}

// OptionsFromBlackboard builds the snapshot of the options given to scripts.
func OptionsFromBlackboard(bb domain.Blackboard) []Option {
	out := []Option{}
	for _, o := range domain.OptionsOf(bb) {
		v := Option{ID: o.ID, Status: o.OptionStatus(), Active: o.Active, Evaluation: o.Evaluation}
		if o.Option != nil {
			v.Name, v.Hypothesis = o.Option.Name, o.Option.Hypothesis
		}
		out = append(out, v)
	}
	return out
}

// DecisionPointsFromBlackboard builds the snapshot of the decision points given to scripts.
func DecisionPointsFromBlackboard(bb domain.Blackboard) []DecisionPoint {
	out := []DecisionPoint{}
	for _, d := range domain.DecisionPointsOf(bb) {
		v := DecisionPoint{ID: d.ID, Question: d.Question, Options: append([]string{}, d.Options...), Criteria: append([]string{}, d.Criteria...),
			Policy: maps.Clone(d.Policy), Status: d.Status, Option: d.Option, Questions: []Question{}}
		if v.Policy == nil {
			v.Policy = map[string]any{}
		}
		for _, q := range d.Questions {
			v.Questions = append(v.Questions, Question{ID: q.ID, Point: q.Point, Text: q.Text, Status: q.Status, Answer: q.Answer})
		}
		out = append(out, v)
	}
	return out
}

// Options returns the options of the change.
func (c *Ctx) Options() []Option {
	if c.job.Options == nil {
		return []Option{}
	}
	return c.job.Options
}

// DecisionPoints returns the decision points of the change.
func (c *Ctx) DecisionPoints() []DecisionPoint {
	if c.job.DecisionPoints == nil {
		return []DecisionPoint{}
	}
	return c.job.DecisionPoints
}

// OpenDecision opens a decision point on a question; spec may hold options (names or ids; none: the open options),
// criteria; any other key is a policy value of the point (decider, threshold, maxRounds, maxDuration ("48h"): the
// keys of pkg/decision, which the script does not interpret). It returns a reference ("#dN") for the rulings of the
// same script.
func (c *Ctx) OpenDecision(question string, spec map[string]any) string {
	c.dseq++
	ref := fmt.Sprintf("#d%d", c.dseq)
	op := map[string]any{"op": domain.DecisionOpenOp, "ref": ref, "question": question}
	policy := map[string]any{}
	for k, v := range spec {
		switch k {
		case "options", "criteria":
			op[k] = v
		default:
			policy[k] = v
		}
	}
	if len(policy) > 0 {
		op["policy"] = policy
	}
	c.emit(map[string]any{"kind": "decisionPoint", "decisionPoint": op})
	return ref
}

// Decide rules a decision point decided: the option (name or id), the confidence (0 to 1) and why. point is an id,
// a "#dN" reference, or "" for the only pending point.
func (c *Ctx) Decide(point, option string, confidence float64, justification string) {
	c.emit(map[string]any{"kind": "decisionPoint", "decisionPoint": map[string]any{"op": domain.DecisionRuleOp, "point": point,
		"outcome": domain.OutcomeDecided, "option": option, "confidence": confidence, "justification": justification}})
}

// Undecidable rules a decision point undecidable: why, and the questions that must be answered first.
func (c *Ctx) Undecidable(point, justification string, questions []string) {
	c.emit(map[string]any{"kind": "decisionPoint", "decisionPoint": map[string]any{"op": domain.DecisionRuleOp, "point": point,
		"outcome": domain.OutcomeUndecidable, "justification": justification, "questions": questions}})
}

// Answer answers an open question of a decision point.
func (c *Ctx) Answer(questionID, answer string) {
	c.emit(map[string]any{"kind": "decisionPoint", "decisionPoint": map[string]any{"op": domain.DecisionAnswerOp, "questionId": questionID,
		"answer": answer}})
}
