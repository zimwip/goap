package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
)

// DecisionEventToPB converts an event of a decision point (ADR 0009 §4).
func DecisionEventToPB(e domain.DecisionEvent) *graphv1.DecisionEvent {
	return &graphv1.DecisionEvent{Op: e.Op, Point: e.Point, Question: e.Question, Options: e.Options, Criteria: e.Criteria,
		Policy: Struct(e.Policy), Outcome: e.Outcome, Option: e.Option,
		Confidence: e.Confidence, Justification: e.Justification, Questions: e.Questions, Human: e.Human, QuestionId: e.QuestionID,
		Answer: e.Answer, Process: e.Process, Accept: e.Accept, Comment: e.Comment, By: e.By}
}

// DecisionEventFromPB converts an event of a decision point.
func DecisionEventFromPB(e *graphv1.DecisionEvent) domain.DecisionEvent {
	return domain.DecisionEvent{Op: e.Op, Point: e.Point, Question: e.Question, Options: e.Options, Criteria: e.Criteria,
		Policy: Map(e.Policy), Outcome: e.Outcome, Option: e.Option,
		Confidence: e.Confidence, Justification: e.Justification, Questions: e.Questions, Human: e.Human, QuestionID: e.QuestionId,
		Answer: e.Answer, Process: e.Process, Accept: e.Accept, Comment: e.Comment, By: e.By}
}

// DecisionPointToPB converts a replayed decision point.
func DecisionPointToPB(d domain.DecisionPoint) *graphv1.DecisionPoint {
	out := &graphv1.DecisionPoint{Id: d.ID, Question: d.Question, Options: d.Options, Criteria: d.Criteria, Policy: Struct(d.Policy),
		OpenedAt: Time(d.OpenedAt), OpenedBy: d.OpenedBy, Status: d.Status, HumanOnly: d.HumanOnly, Escalation: d.Escalation, Option: d.Option, DecidedAt: Time(d.DecidedAt), DecidedBy: d.DecidedBy}
	for _, q := range d.Questions {
		out.Questions = append(out.Questions, &graphv1.Question{Id: q.ID, Point: q.Point, Text: q.Text, Status: q.Status, Answer: q.Answer,
			AnsweredBy: q.AnsweredBy, Process: q.Process, AskedAt: Time(q.AskedAt)})
	}
	if r := d.Ruling; r != nil {
		out.Ruling = &graphv1.Ruling{Outcome: r.Outcome, Option: r.Option, Confidence: r.Confidence, Justification: r.Justification, By: r.By,
			Human: r.Human, At: Time(r.At)}
	}
	return out
}

// DecisionPointFromPB converts a replayed decision point.
func DecisionPointFromPB(d *graphv1.DecisionPoint) domain.DecisionPoint {
	if d == nil {
		return domain.DecisionPoint{}
	}
	out := domain.DecisionPoint{ID: d.Id, Question: d.Question, Options: d.Options, Criteria: d.Criteria, Policy: Map(d.Policy),
		OpenedAt: FromTime(d.OpenedAt), OpenedBy: d.OpenedBy, Status: d.Status, HumanOnly: d.HumanOnly, Escalation: d.Escalation, Option: d.Option, DecidedAt: FromTime(d.DecidedAt), DecidedBy: d.DecidedBy}
	for _, q := range d.Questions {
		out.Questions = append(out.Questions, domain.Question{ID: q.Id, Point: q.Point, Text: q.Text, Status: q.Status, Answer: q.Answer,
			AnsweredBy: q.AnsweredBy, Process: q.Process, AskedAt: FromTime(q.AskedAt)})
	}
	if r := d.Ruling; r != nil {
		out.Ruling = &domain.Ruling{Outcome: r.Outcome, Option: r.Option, Confidence: r.Confidence, Justification: r.Justification, By: r.By,
			Human: r.Human, At: FromTime(r.At)}
	}
	return out
}
