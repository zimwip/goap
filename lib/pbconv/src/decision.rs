use goap_domain as domain;
use goap_gen::graph::v1 as pb;

use crate::{from_opt_time, from_time, opt_time, time, to_map, to_struct};

/// DecisionEventToPB converts an event of a decision point (ADR 0009 §4).
pub fn decision_event_to_pb(e: &domain::DecisionEvent) -> pb::DecisionEvent {
    pb::DecisionEvent {
        op: e.op.clone(),
        point: e.point.clone(),
        question: e.question.clone(),
        options: e.options.clone(),
        criteria: e.criteria.clone(),
        policy: to_struct(&e.policy),
        outcome: e.outcome.clone(),
        option: e.option.clone(),
        confidence: e.confidence,
        justification: e.justification.clone(),
        questions: e.questions.clone(),
        human: e.human,
        question_id: e.question_id.clone(),
        answer: e.answer.clone(),
        process: e.process.clone(),
        accept: e.accept,
        comment: e.comment.clone(),
        by: e.by.clone(),
    }
}

/// DecisionEventFromPB converts an event of a decision point.
pub fn decision_event_from_pb(e: &pb::DecisionEvent) -> domain::DecisionEvent {
    domain::DecisionEvent {
        op: e.op.clone(),
        point: e.point.clone(),
        question: e.question.clone(),
        options: e.options.clone(),
        criteria: e.criteria.clone(),
        policy: to_map(e.policy.as_ref()),
        outcome: e.outcome.clone(),
        option: e.option.clone(),
        confidence: e.confidence,
        justification: e.justification.clone(),
        questions: e.questions.clone(),
        human: e.human,
        question_id: e.question_id.clone(),
        answer: e.answer.clone(),
        process: e.process.clone(),
        accept: e.accept,
        comment: e.comment.clone(),
        by: e.by.clone(),
    }
}

/// DecisionPointToPB converts a replayed decision point.
pub fn decision_point_to_pb(d: &domain::DecisionPoint) -> pb::DecisionPoint {
    pb::DecisionPoint {
        id: d.id.clone(),
        question: d.question.clone(),
        options: d.options.clone(),
        criteria: d.criteria.clone(),
        policy: to_struct(&d.policy),
        opened_at: time(&d.opened_at),
        opened_by: d.opened_by.clone(),
        status: d.status.clone(),
        human_only: d.human_only,
        escalation: d.escalation.clone(),
        option: d.option.clone(),
        decided_at: opt_time(&d.decided_at),
        decided_by: d.decided_by.clone(),
        questions: d
            .questions
            .iter()
            .map(|q| pb::Question {
                id: q.id.clone(),
                point: q.point.clone(),
                text: q.text.clone(),
                status: q.status.clone(),
                answer: q.answer.clone(),
                answered_by: q.answered_by.clone(),
                process: q.process.clone(),
                asked_at: time(&q.asked_at),
            })
            .collect(),
        ruling: d.ruling.as_ref().map(|r| pb::Ruling {
            outcome: r.outcome.clone(),
            option: r.option.clone(),
            confidence: r.confidence,
            justification: r.justification.clone(),
            by: r.by.clone(),
            human: r.human,
            at: time(&r.at),
        }),
    }
}

/// DecisionPointFromPB converts a replayed decision point (the zero point for None).
pub fn decision_point_from_pb(d: Option<&pb::DecisionPoint>) -> domain::DecisionPoint {
    let Some(d) = d else {
        return domain::DecisionPoint::default();
    };
    domain::DecisionPoint {
        id: d.id.clone(),
        question: d.question.clone(),
        options: d.options.clone(),
        criteria: d.criteria.clone(),
        policy: to_map(d.policy.as_ref()),
        opened_at: from_time(d.opened_at.as_ref()),
        opened_by: d.opened_by.clone(),
        status: d.status.clone(),
        human_only: d.human_only,
        escalation: d.escalation.clone(),
        option: d.option.clone(),
        decided_at: from_opt_time(d.decided_at.as_ref()),
        decided_by: d.decided_by.clone(),
        questions: d
            .questions
            .iter()
            .map(|q| domain::Question {
                id: q.id.clone(),
                point: q.point.clone(),
                text: q.text.clone(),
                status: q.status.clone(),
                answer: q.answer.clone(),
                answered_by: q.answered_by.clone(),
                process: q.process.clone(),
                asked_at: from_time(q.asked_at.as_ref()),
            })
            .collect(),
        ruling: d.ruling.as_ref().map(|r| domain::Ruling {
            outcome: r.outcome.clone(),
            option: r.option.clone(),
            confidence: r.confidence,
            justification: r.justification.clone(),
            by: r.by.clone(),
            human: r.human,
            at: from_time(r.at.as_ref()),
        }),
    }
}
