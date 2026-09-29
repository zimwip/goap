package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// Decision points of a change (ADR 0009 §4): opened on the question to settle, ruled by their decider (decided, or
// undecidable with the questions that block it), the questions answered, an agent ruling below the threshold
// ratified by a person. A decided point that names an option selects it (ADR 0032 §6). Every operation is a
// KindDecisionPoint fact of the main flow; the state is its replay (domain.Change.DecisionPointsAt).

// Defaults of a decision point.
const (
	DefaultDecisionThreshold = 0.7
	DefaultDecisionRounds    = 3
)

// OpenDecisionRequest opens a decision point.
type OpenDecisionRequest struct {
	Question string
	// Options are the options it chooses among; nil: the open options of the change, [] none (a free question).
	Options  []string
	Criteria []string
	// Decider is agent (default) or human.
	Decider string
	// Threshold is the confidence under which an agent ruling waits for a human ratification (default 0.7).
	Threshold float64
	// MaxRounds is the number of rulings that may fail to settle the point before it is escalated (default 3).
	MaxRounds int
	// MaxDuration escalates the point once passed (0: no deadline).
	MaxDuration time.Duration
	By          string
}

// RuleRequest is a ruling of a decision point.
type RuleRequest struct {
	Point string
	// Outcome is decided (Option, Confidence) or undecidable (Questions: what blocks it).
	Outcome       string
	Option        string
	Confidence    float64
	Justification string
	Questions     []string
	// Human tells the ruling comes from a person: it needs no ratification, and it is the only one an escalated
	// point or a point with a human decider accepts.
	Human bool
	By    string
}

// decisionEvent appends a decision event to the log of a change, on the main flow.
func (g *Graph) decisionEvent(ctx context.Context, tx Tx, id domain.ChangeID, e domain.DecisionEvent) error {
	_, err := g.putDecisionEvent(ctx, tx, id, e)
	return err
}

func (g *Graph) putDecisionEvent(ctx context.Context, tx Tx, id domain.ChangeID, e domain.DecisionEvent) (domain.ItemID, error) {
	it := domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindDecisionPoint, Type: "decision." + e.Op, Status: domain.ItemAccepted,
		ProducedBy: firstNonEmpty(e.By, "graph.decision"), DecisionEvent: &e, CreatedAt: g.now()}
	if err := it.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", err, ErrInvalid)
	}
	return it.ID, putItem(ctx, tx, id, it)
}

// OpenDecision opens a decision point on a change.
func (g *Graph) OpenDecision(ctx context.Context, id domain.ChangeID, in OpenDecisionRequest) (d domain.DecisionPoint, err error) {
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return d, fmt.Errorf("a decision point needs a question: %w", ErrInvalid)
	}
	switch in.Decider {
	case "":
		in.Decider = domain.DeciderAgent
	case domain.DeciderAgent, domain.DeciderHuman:
	default:
		return d, fmt.Errorf("decider %q: agent or human: %w", in.Decider, ErrInvalid)
	}
	if in.Threshold == 0 {
		in.Threshold = DefaultDecisionThreshold
	}
	if in.Threshold < 0 || in.Threshold > 1 {
		return d, fmt.Errorf("a confidence threshold is between 0 and 1, not %v: %w", in.Threshold, ErrInvalid)
	}
	if in.MaxRounds == 0 {
		in.MaxRounds = DefaultDecisionRounds
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		options := slices.Clone(in.Options)
		if options == nil {
			for _, o := range c.Options() {
				if o.Status == domain.FlowOpen {
					options = append(options, o.ID)
				}
			}
		}
		for i, o := range options {
			options[i] = optionID(c, o)
			if _, err := openOption(c, options[i]); err != nil {
				return err
			}
		}
		e := domain.DecisionEvent{Op: domain.DecisionOpenOp, Question: q, Options: options, Criteria: in.Criteria, Decider: in.Decider,
			Threshold: in.Threshold, MaxRounds: in.MaxRounds, By: in.By}
		if in.MaxDuration > 0 {
			e.Deadline = g.now().Add(in.MaxDuration)
		}
		pid, err := g.putDecisionEvent(ctx, tx, id, e)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, c); err != nil {
				return err
			}
		}
		d, err = g.decisionOf(ctx, tx, id, string(pid))
		return err
	})
	return
}

// RuleDecision records a ruling. A decided ruling of a person, or of an agent at or above the threshold, decides the
// point and selects its option; below the threshold it waits for a ratification. An undecidable ruling raises its
// questions: the point is blocked until they are answered.
func (g *Graph) RuleDecision(ctx context.Context, id domain.ChangeID, in RuleRequest) (d domain.DecisionPoint, err error) {
	in.Justification = strings.TrimSpace(in.Justification)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		cur, err := pendingPoint(c, in.Point, g.now())
		if err != nil {
			return err
		}
		switch {
		case cur.Status == domain.PointBlocked:
			return fmt.Errorf("decision point %s has %d open question(s): answer them first: %w", cur.ID, cur.OpenQuestions(), ErrConflict)
		case cur.NeedsHuman() && !in.Human:
			why := "its decider is a person"
			if cur.Escalation != "" {
				why = "it is escalated (" + cur.Escalation + ")"
			}
			return fmt.Errorf("decision point %s is ruled by a person: %s: %w", cur.ID, why, ErrConflict)
		case cur.Status == domain.PointRatifying && !in.Human:
			return fmt.Errorf("decision point %s waits for the ratification of its ruling: %w", cur.ID, ErrConflict)
		}
		e := domain.DecisionEvent{Op: domain.DecisionRuleOp, Point: cur.ID, Outcome: in.Outcome, Justification: in.Justification, Human: in.Human, By: in.By}
		switch in.Outcome {
		case domain.OutcomeDecided:
			if in.Option != "" {
				in.Option = optionID(c, in.Option)
			}
			if len(cur.Options) > 0 && !slices.Contains(cur.Options, in.Option) {
				return fmt.Errorf("decision point %s chooses among %v, not %q: %w", cur.ID, cur.Options, in.Option, ErrInvalid)
			}
			if in.Confidence < 0 || in.Confidence > 1 {
				return fmt.Errorf("a confidence is between 0 and 1, not %v: %w", in.Confidence, ErrInvalid)
			}
			if in.Justification == "" {
				return fmt.Errorf("a decision needs a justification: %w", ErrInvalid)
			}
			e.Option, e.Confidence = in.Option, in.Confidence
		case domain.OutcomeUndecidable:
			for _, q := range in.Questions {
				if q = strings.TrimSpace(q); q != "" {
					e.Questions = append(e.Questions, q)
				}
			}
			if len(e.Questions) == 0 || in.Justification == "" {
				return fmt.Errorf("an undecidable ruling says why and asks at least one question: %w", ErrInvalid)
			}
		default:
			return fmt.Errorf("a ruling is %s or %s, not %q: %w", domain.OutcomeDecided, domain.OutcomeUndecidable, in.Outcome, ErrInvalid)
		}
		if err := g.decisionEvent(ctx, tx, id, e); err != nil {
			return err
		}
		d, err = g.settleDecision(ctx, tx, id, cur.ID, in.By)
		return err
	})
	return
}

// AnswerQuestion answers an open question of a decision point; process is the one that investigated it, if any.
func (g *Graph) AnswerQuestion(ctx context.Context, id domain.ChangeID, question, answer, process, by string) (d domain.DecisionPoint, err error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return d, fmt.Errorf("an answer needs a text: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		point := ""
		for _, p := range c.DecisionPointsAt(g.now()) {
			for _, q := range p.Questions {
				if q.ID != question {
					continue
				}
				if q.Status != domain.QuestionOpen {
					return fmt.Errorf("question %s is %s: %w", question, q.Status, ErrConflict)
				}
				point = p.ID
			}
		}
		if point == "" {
			return fmt.Errorf("question %s of change %s: %w", question, id, ErrNotFound)
		}
		if err := g.decisionEvent(ctx, tx, id, domain.DecisionEvent{Op: domain.DecisionAnswerOp, Point: point, QuestionID: question, Answer: answer,
			Process: process, By: by}); err != nil {
			return err
		}
		d, err = g.decisionOf(ctx, tx, id, point)
		return err
	})
	return
}

// RatifyDecision accepts or refuses the ruling an agent made below the threshold of the point (a person's act).
// Accepted, it decides the point; refused, it counts as a round and the point is open again.
func (g *Graph) RatifyDecision(ctx context.Context, id domain.ChangeID, point string, accept bool, by, comment string) (d domain.DecisionPoint, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := flowChange(ctx, tx, id)
		if err != nil {
			return err
		}
		cur, err := pendingPoint(c, point, g.now())
		if err != nil {
			return err
		}
		if cur.Status != domain.PointRatifying {
			return fmt.Errorf("decision point %s is %s: nothing to ratify: %w", point, cur.Status, ErrConflict)
		}
		if !accept && strings.TrimSpace(comment) == "" {
			return fmt.Errorf("refusing a ruling needs a comment: %w", ErrInvalid)
		}
		if err := g.decisionEvent(ctx, tx, id, domain.DecisionEvent{Op: domain.DecisionRatifyOp, Point: point, Accept: accept, Comment: comment,
			Human: true, By: by}); err != nil {
			return err
		}
		d, err = g.settleDecision(ctx, tx, id, point, by)
		return err
	})
	return
}

// DecisionPoints lists the decision points of a change, oldest first.
func (g *Graph) DecisionPoints(ctx context.Context, id domain.ChangeID) (out []domain.DecisionPoint, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		out = c.DecisionPointsAt(g.now())
		return nil
	})
	return
}

// settleDecision finalizes a point the last event decided (ADR 0009 §5): its option is selected, what the option
// wrote joins the change branch and the other open options are rejected.
func (g *Graph) settleDecision(ctx context.Context, tx Tx, id domain.ChangeID, point, by string) (domain.DecisionPoint, error) {
	d, err := g.decisionOf(ctx, tx, id, point)
	if err != nil || d.Status != domain.PointDecided || d.Option == "" {
		return d, err
	}
	c, err := tx.Change(ctx, id)
	if err != nil {
		return d, err
	}
	if f, ok := c.Flow(d.Option); !ok || f.Option == nil || f.Status != domain.FlowOpen {
		return d, nil // not an option of the change, or already decided
	}
	return d, g.selectOptionTx(ctx, tx, c, d.Option, by)
}

func (g *Graph) decisionOf(ctx context.Context, tx Tx, id domain.ChangeID, point string) (domain.DecisionPoint, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.DecisionPoint{}, err
	}
	d, ok := c.DecisionPoint(point, g.now())
	if !ok {
		return d, fmt.Errorf("decision point %s of change %s: %w", point, id, ErrNotFound)
	}
	return d, nil
}

// optionID resolves an option of c by id or by name (the names of the open options first).
func optionID(c domain.Change, s string) string {
	options := c.Options()
	for _, o := range options {
		if o.ID == s {
			return s
		}
	}
	for _, open := range []bool{true, false} {
		for _, o := range options {
			if (o.Status == domain.FlowOpen) == open && strings.EqualFold(o.Option.Name, s) {
				return o.ID
			}
		}
	}
	return s
}

func pendingPoint(c domain.Change, point string, now time.Time) (domain.DecisionPoint, error) {
	if point == "" { // the only pending point
		var pending []domain.DecisionPoint
		for _, d := range c.DecisionPointsAt(now) {
			if d.Pending() {
				pending = append(pending, d)
			}
		}
		if len(pending) != 1 {
			return domain.DecisionPoint{}, fmt.Errorf("change %s has %d pending decision points: name one: %w", c.ID, len(pending), ErrInvalid)
		}
		return pending[0], nil
	}
	d, ok := c.DecisionPoint(point, now)
	if !ok {
		return d, fmt.Errorf("decision point %s of change %s: %w", point, c.ID, ErrNotFound)
	}
	if !d.Pending() {
		return d, fmt.Errorf("decision point %s is decided: %w", point, ErrConflict)
	}
	return d, nil
}

// pendingDecisions are the decision points of c not decided yet.
func pendingDecisions(c domain.Change, now time.Time) []domain.DecisionPoint {
	return slices.DeleteFunc(c.DecisionPointsAt(now), func(d domain.DecisionPoint) bool { return !d.Pending() })
}
