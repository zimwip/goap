package domain

import (
	"fmt"
	"slices"
	"time"
)

// Decision points (ADR 0009 §4): a question the change must answer, usually "which option", decided by an agent or
// a human. The decider rules: decided (an option, with a confidence and a justification) or undecidable, with the
// questions that block it. Open questions block the point; answering them (by hand, or by investigating them in a
// sub-agent) reopens it, and the planner comes back to the decision: the loop emerges from the blackboard. An agent
// ruling below the confidence threshold of the point waits for a human ratification. Safeguards: after MaxRounds
// undecidable rulings, or past the deadline, the point is escalated: only a human may rule it.
//
// Like the flows, a decision point is a replay of events: KindDecisionPoint items of the main flow.

// KindDecisionPoint is an event of a decision point (DecisionEvent).
const KindDecisionPoint ItemKind = "decision_point"

// Decision event operations.
const (
	DecisionOpenOp   = "open"
	DecisionRuleOp   = "rule"
	DecisionAnswerOp = "answer"
	DecisionRatifyOp = "ratify"
)

// Rulings and deciders.
const (
	OutcomeDecided     = "decided"
	OutcomeUndecidable = "undecidable"
	DeciderAgent       = "agent"
	DeciderHuman       = "human"
)

// Decision point statuses.
const (
	PointOpen      = "open"      // waiting for a ruling
	PointBlocked   = "blocked"   // open questions wait for their answer
	PointRatifying = "ratifying" // an agent ruled below the threshold: a human ratifies
	PointEscalated = "escalated" // too many rounds or past the deadline: a human rules
	PointDecided   = "decided"
)

// Question statuses.
const (
	QuestionOpen     = "open"
	QuestionAnswered = "answered"
)

// DecisionEvent is the payload of a KindDecisionPoint item.
type DecisionEvent struct {
	Op string `json:"op"`
	// Point is the decision point: the id of the item that opened it (empty on open).
	Point string `json:"point,omitempty"`
	// open
	Question  string    `json:"question,omitempty"`
	Options   []string  `json:"options,omitempty"`
	Criteria  []string  `json:"criteria,omitempty"`
	Decider   string    `json:"decider,omitempty"`
	Threshold float64   `json:"threshold,omitempty"`
	MaxRounds int       `json:"maxRounds,omitempty"`
	Deadline  time.Time `json:"deadline,omitempty"`
	// rule
	Outcome       string   `json:"outcome,omitempty"`
	Option        string   `json:"option,omitempty"`
	Confidence    float64  `json:"confidence,omitempty"`
	Justification string   `json:"justification,omitempty"`
	Questions     []string `json:"questions,omitempty"`
	// Human tells a ruling or a ratification comes from a person, not an agent.
	Human bool `json:"human,omitempty"`
	// answer: the question and its answer; the process that investigated it, if any
	QuestionID string `json:"questionId,omitempty"`
	Answer     string `json:"answer,omitempty"`
	Process    string `json:"process,omitempty"`
	// ratify
	Accept  bool   `json:"accept,omitempty"`
	Comment string `json:"comment,omitempty"`
	By      string `json:"by,omitempty"`
}

func (e *DecisionEvent) validate() error {
	if e == nil {
		return fmt.Errorf("decision point item requires decisionEvent")
	}
	switch e.Op {
	case DecisionOpenOp:
		if e.Question == "" {
			return fmt.Errorf("a decision point needs a question")
		}
	case DecisionRuleOp, DecisionRatifyOp:
		if e.Point == "" {
			return fmt.Errorf("decision %s requires a point", e.Op)
		}
	case DecisionAnswerOp:
		if e.Point == "" || e.QuestionID == "" {
			return fmt.Errorf("an answer requires a point and a question")
		}
	default:
		return fmt.Errorf("unknown decision op %q", e.Op)
	}
	return nil
}

// Question is a question raised by an undecidable ruling: it blocks its decision point until answered.
type Question struct {
	ID         string    `json:"id"`
	Point      string    `json:"point"`
	Text       string    `json:"text"`
	Status     string    `json:"status"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredBy string    `json:"answeredBy,omitempty"`
	Process    string    `json:"process,omitempty"`
	AskedAt    time.Time `json:"askedAt"`
}

// Ruling is the last ruling of a decision point.
type Ruling struct {
	Outcome       string    `json:"outcome"`
	Option        string    `json:"option,omitempty"`
	Confidence    float64   `json:"confidence,omitempty"`
	Justification string    `json:"justification,omitempty"`
	By            string    `json:"by,omitempty"`
	Human         bool      `json:"human,omitempty"`
	At            time.Time `json:"at"`
}

// DecisionPoint is a decision point, replayed from its events.
type DecisionPoint struct {
	ID        string    `json:"id"`
	Question  string    `json:"question"`
	Options   []string  `json:"options,omitempty"`
	Criteria  []string  `json:"criteria,omitempty"`
	Decider   string    `json:"decider"`
	Threshold float64   `json:"threshold"`
	MaxRounds int       `json:"maxRounds"`
	Deadline  time.Time `json:"deadline,omitempty"`
	OpenedAt  time.Time `json:"openedAt"`
	OpenedBy  string    `json:"openedBy,omitempty"`
	Status    string    `json:"status"`
	// Rounds counts the rulings that did not settle the point: undecidable, or a ratification refused.
	Rounds    int        `json:"rounds"`
	Questions []Question `json:"questions,omitempty"`
	Ruling    *Ruling    `json:"ruling,omitempty"`
	// Escalation says why only a human may rule the point now ("" = not escalated).
	Escalation string `json:"escalation,omitempty"`
	// Option and DecidedAt / DecidedBy are set once decided.
	Option    string    `json:"option,omitempty"`
	DecidedAt time.Time `json:"decidedAt,omitempty"`
	DecidedBy string    `json:"decidedBy,omitempty"`
}

// OpenQuestions counts the questions waiting for an answer.
func (d DecisionPoint) OpenQuestions() int {
	n := 0
	for _, q := range d.Questions {
		if q.Status == QuestionOpen {
			n++
		}
	}
	return n
}

// Pending reports whether the point is not decided yet.
func (d DecisionPoint) Pending() bool { return d.Status != PointDecided }

// NeedsHuman reports whether only a person may rule the point: a human decider, or an escalated point.
func (d DecisionPoint) NeedsHuman() bool { return d.Decider == DeciderHuman || d.Escalation != "" }

// QuestionID names the i-th question raised by the ruling item rule.
func QuestionID(rule ItemID, i int) string { return fmt.Sprintf("%s:%d", rule, i+1) }

// DecisionPointsAt replays the decision points of the change, oldest first, at a moment (the deadlines are judged
// against it). Only the events of the main flow count.
func (c *Change) DecisionPointsAt(now time.Time) []DecisionPoint {
	var out []DecisionPoint
	at := map[string]int{}
	for _, it := range c.Items {
		e := it.DecisionEvent
		if it.Kind != KindDecisionPoint || e == nil || it.Flow != "" {
			continue
		}
		if e.Op == DecisionOpenOp {
			d := DecisionPoint{ID: string(it.ID), Question: e.Question, Options: slices.Clone(e.Options), Criteria: slices.Clone(e.Criteria),
				Decider: e.Decider, Threshold: e.Threshold, MaxRounds: e.MaxRounds, Deadline: e.Deadline, OpenedAt: it.CreatedAt, OpenedBy: e.By}
			if d.Decider == "" {
				d.Decider = DeciderAgent
			}
			at[d.ID] = len(out)
			out = append(out, d)
			continue
		}
		i, ok := at[e.Point]
		if !ok {
			continue
		}
		d := &out[i]
		if d.DecidedBy != "" || !d.DecidedAt.IsZero() {
			continue // a decided point does not move
		}
		switch e.Op {
		case DecisionRuleOp:
			r := &Ruling{Outcome: e.Outcome, Option: e.Option, Confidence: e.Confidence, Justification: e.Justification, By: e.By, Human: e.Human, At: it.CreatedAt}
			d.Ruling = r
			switch {
			case e.Outcome == OutcomeUndecidable:
				d.Rounds++
				for j, q := range e.Questions {
					d.Questions = append(d.Questions, Question{ID: QuestionID(it.ID, j), Point: d.ID, Text: q, Status: QuestionOpen, AskedAt: it.CreatedAt})
				}
			case e.Human || e.Confidence >= d.Threshold:
				d.decide(r)
			}
		case DecisionRatifyOp:
			if d.Ruling == nil || d.Ruling.Outcome != OutcomeDecided {
				continue
			}
			if e.Accept {
				d.decide(&Ruling{Outcome: OutcomeDecided, Option: d.Ruling.Option, Confidence: d.Ruling.Confidence, Justification: d.Ruling.Justification,
					By: e.By, Human: true, At: it.CreatedAt})
			} else {
				d.Rounds++
				d.Ruling = nil
			}
		case DecisionAnswerOp:
			for j := range d.Questions {
				if q := &d.Questions[j]; q.ID == e.QuestionID && q.Status == QuestionOpen {
					q.Status, q.Answer, q.AnsweredBy, q.Process = QuestionAnswered, e.Answer, e.By, e.Process
				}
			}
		}
	}
	for i := range out {
		out[i].settle(now)
	}
	return out
}

func (d *DecisionPoint) decide(r *Ruling) {
	d.Ruling, d.Option, d.DecidedAt, d.DecidedBy = r, r.Option, r.At, r.By
}

// settle derives the status of a replayed point.
func (d *DecisionPoint) settle(now time.Time) {
	if !d.DecidedAt.IsZero() {
		d.Status = PointDecided
		return
	}
	switch {
	case d.MaxRounds > 0 && d.Rounds >= d.MaxRounds:
		d.Escalation = fmt.Sprintf("%d rounds without a decision", d.Rounds)
	case !d.Deadline.IsZero() && now.After(d.Deadline):
		d.Escalation = "past the deadline " + d.Deadline.UTC().Format(time.RFC3339)
	}
	switch {
	case d.Ruling != nil && d.Ruling.Outcome == OutcomeDecided:
		d.Status = PointRatifying
	case d.OpenQuestions() > 0:
		d.Status = PointBlocked
	case d.Escalation != "":
		d.Status = PointEscalated
	default:
		d.Status = PointOpen
	}
}

// DecisionPoint returns a decision point replayed at now.
func (c *Change) DecisionPoint(id string, now time.Time) (DecisionPoint, bool) {
	for _, d := range c.DecisionPointsAt(now) {
		if d.ID == id {
			return d, true
		}
	}
	return DecisionPoint{}, false
}
