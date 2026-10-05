package domain

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Decision points (ADR 0009 §4): a question the change must answer, usually "which option". A ruling is decided (an
// option, with a confidence and a justification) or undecidable, with the questions that block it. Open questions
// block the point; answering them (by hand, or by investigating them in a sub-agent) reopens it, and the planner
// comes back to the decision: the loop emerges from the blackboard. A decided ruling that does not settle the point
// waits for a human ratification; a point may be reserved to a person (escalated, or by design).
//
// This is the mechanism: open, rule, answer, ratify, who ruled, the status machine. WHEN a ruling settles the point
// and WHEN it is reserved to a person (a confidence threshold, a number of rounds, a deadline) is a policy, a
// DecisionPolicy (ADR 0067): the graph holds the opaque policy values the opener gave (DecisionPoint.Policy) and asks
// the policy. pkg/decision is the one the services plug.
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

// Rulings.
const (
	OutcomeDecided     = "decided"
	OutcomeUndecidable = "undecidable"
)

// Decision point statuses.
const (
	PointOpen      = "open"      // waiting for a ruling
	PointBlocked   = "blocked"   // open questions wait for their answer
	PointRatifying = "ratifying" // a decided ruling did not settle the point: a human ratifies
	PointEscalated = "escalated" // the policy reserved the point to a person: a human rules
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
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Criteria []string `json:"criteria,omitempty"`
	// Policy is the opaque policy values of the point, as the DecisionPolicy resolved them at the opening: the
	// graph stores them and hands them back to the policy, never reads them.
	Policy map[string]any `json:"policy,omitempty"`
	// rule
	Outcome       string   `json:"outcome,omitempty"`
	Option        string   `json:"option,omitempty"`
	Confidence    float64  `json:"confidence,omitempty"`
	Justification string   `json:"justification,omitempty"`
	Questions     []string `json:"questions,omitempty"`
	// Human tells a ruling or a ratification comes from a person, not an agent. The confidence of a ruling is
	// recorded as given, the policy reads it.
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
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Criteria []string `json:"criteria,omitempty"`
	// Policy is the policy values of the point (see DecisionEvent.Policy), updated by the policy as it folds the
	// events (a count of rounds, say): a private copy, replayed with the point.
	Policy    map[string]any `json:"policy,omitempty"`
	OpenedAt  time.Time      `json:"openedAt"`
	OpenedBy  string         `json:"openedBy,omitempty"`
	Status    string         `json:"status"`
	Questions []Question     `json:"questions,omitempty"`
	Ruling    *Ruling        `json:"ruling,omitempty"`
	// HumanOnly tells only a person may rule the point now: by design, or because it is escalated.
	HumanOnly bool `json:"humanOnly,omitempty"`
	// Escalation says why the policy reserved the point to a person ("" = not escalated).
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

// NeedsHuman reports whether only a person may rule the point: reserved to one by design, or escalated.
func (d DecisionPoint) NeedsHuman() bool { return d.HumanOnly || d.Escalation != "" }

// DecisionPolicy is the rule that says when a ruling settles a decision point and when only a person may rule it. It
// is pure and deterministic: DecisionPointsAt replays the log with it, at any time, and must find the same points.
// The graph core has no policy of its own beyond DefaultDecisionPolicy; pkg/decision is the confidence / rounds /
// deadline policy of the platform (ADR 0067).
type DecisionPolicy interface {
	// Open resolves the policy values the opener gave: defaults, validation, values relative to now (a deadline).
	// The result is stored on the opening event as DecisionPoint.Policy; an error refuses the opening.
	Open(given map[string]any, now time.Time) (map[string]any, error)
	// Fold takes an event of a point into account, after the core recorded it, and says whether it settles the
	// point: a rule (decided or undecidable) or a ratify (accepted or refused). It may update p.Policy, which is a
	// private copy.
	Fold(p *DecisionPoint, ev DecisionEvent) (settled bool)
	// Reserve says whether only a person may rule the point at now (humanOnly), and why when the point is escalated.
	// It is asked of the points that are not decided.
	Reserve(p DecisionPoint, now time.Time) (humanOnly bool, escalation string)
}

// DefaultDecisionPolicy is the policy of a graph that was given none: a decided ruling of anyone settles the point,
// an accepted ratification settles it, nothing is ever reserved to a person.
func DefaultDecisionPolicy() DecisionPolicy { return minimalPolicy{} }

type minimalPolicy struct{}

func (minimalPolicy) Open(given map[string]any, _ time.Time) (map[string]any, error) {
	if len(given) > 0 {
		return nil, fmt.Errorf("the graph has no decision policy: no policy values (%d given)", len(given))
	}
	return nil, nil
}

func (minimalPolicy) Fold(_ *DecisionPoint, ev DecisionEvent) bool {
	switch ev.Op {
	case DecisionRuleOp:
		return ev.Outcome == OutcomeDecided
	case DecisionRatifyOp:
		return ev.Accept
	}
	return false
}

func (minimalPolicy) Reserve(DecisionPoint, time.Time) (bool, string) { return false, "" }

// QuestionID names the i-th question raised by the ruling item rule.
func QuestionID(rule ItemID, i int) string { return fmt.Sprintf("%s:%d", rule, i+1) }

// DecisionPointsAt replays the decision points of the change, oldest first, at a moment (the policy judges its
// deadlines against it), under a policy (nil: DefaultDecisionPolicy). The policy is a parameter, not a registry: the
// replay stays a pure function of the log, the moment and the policy. Only the events of the main flow count.
func (c *Change) DecisionPointsAt(now time.Time, policy DecisionPolicy) []DecisionPoint {
	if policy == nil {
		policy = DefaultDecisionPolicy()
	}
	var out []DecisionPoint
	at := map[string]int{}
	for _, it := range c.Items {
		e := it.DecisionEvent
		if it.Kind != KindDecisionPoint || e == nil || it.Flow != "" {
			continue
		}
		if e.Op == DecisionOpenOp {
			d := DecisionPoint{ID: string(it.ID), Question: e.Question, Options: slices.Clone(e.Options), Criteria: slices.Clone(e.Criteria),
				Policy: maps.Clone(e.Policy), OpenedAt: it.CreatedAt, OpenedBy: e.By}
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
			settled := policy.Fold(d, *e)
			switch {
			case e.Outcome == OutcomeUndecidable:
				for j, q := range e.Questions {
					d.Questions = append(d.Questions, Question{ID: QuestionID(it.ID, j), Point: d.ID, Text: q, Status: QuestionOpen, AskedAt: it.CreatedAt})
				}
			case settled:
				d.decide(r)
			}
		case DecisionRatifyOp:
			if d.Ruling == nil || d.Ruling.Outcome != OutcomeDecided {
				continue
			}
			if policy.Fold(d, *e) {
				d.decide(&Ruling{Outcome: OutcomeDecided, Option: d.Ruling.Option, Confidence: d.Ruling.Confidence, Justification: d.Ruling.Justification,
					By: e.By, Human: true, At: it.CreatedAt})
			} else {
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
		out[i].settle(now, policy)
	}
	return out
}

func (d *DecisionPoint) decide(r *Ruling) {
	d.Ruling, d.Option, d.DecidedAt, d.DecidedBy = r, r.Option, r.At, r.By
}

// settle derives the status of a replayed point.
func (d *DecisionPoint) settle(now time.Time, policy DecisionPolicy) {
	if !d.DecidedAt.IsZero() {
		d.Status = PointDecided
		return
	}
	d.HumanOnly, d.Escalation = policy.Reserve(*d, now)
	d.HumanOnly = d.HumanOnly || d.Escalation != ""
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

// DecisionPoint returns a decision point replayed at now under a policy (nil: DefaultDecisionPolicy).
func (c *Change) DecisionPoint(id string, now time.Time, policy DecisionPolicy) (DecisionPoint, bool) {
	for _, d := range c.DecisionPointsAt(now, policy) {
		if d.ID == id {
			return d, true
		}
	}
	return DecisionPoint{}, false
}
