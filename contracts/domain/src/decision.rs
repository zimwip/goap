//! Decision points (ADR 0009 §4): a question the change must answer, usually "which option". A ruling is decided (an
//! option, with a confidence and a justification) or undecidable, with the questions that block it. Open questions
//! block the point; answering them (by hand, or by investigating them in a sub-agent) reopens it, and the planner
//! comes back to the decision: the loop emerges from the blackboard. A decided ruling that does not settle the point
//! waits for a human ratification; a point may be reserved to a person (escalated, or by design).
//!
//! This is the mechanism: open, rule, answer, ratify, who ruled, the status machine. WHEN a ruling settles the point
//! and WHEN it is reserved to a person (a confidence threshold, a number of rounds, a deadline) is a policy, a
//! DecisionPolicy (ADR 0067): the graph holds the opaque policy values the opener gave (DecisionPoint.policy) and asks
//! the policy. pkg/decision is the one the services plug.
//!
//! Like the flows, a decision point is a replay of events: KindDecisionPoint items of the main flow.

use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    Change, Error, ItemID, ItemType, gotime,
    serde_util::{is_false, is_zero_f64, null_default},
};

/// TypeDecisionPoint is an event of a decision point (DecisionEvent).
pub const TYPE_DECISION_POINT: ItemType = ItemType::from_static("decision_point");

/// Decision event operations.
pub const DECISION_OPEN_OP: &str = "open";
pub const DECISION_RULE_OP: &str = "rule";
pub const DECISION_ANSWER_OP: &str = "answer";
pub const DECISION_RATIFY_OP: &str = "ratify";

/// Rulings.
pub const OUTCOME_DECIDED: &str = "decided";
pub const OUTCOME_UNDECIDABLE: &str = "undecidable";

/// Decision point statuses: waiting for a ruling.
pub const POINT_OPEN: &str = "open";
/// Open questions wait for their answer.
pub const POINT_BLOCKED: &str = "blocked";
/// A decided ruling did not settle the point: a human ratifies.
pub const POINT_RATIFYING: &str = "ratifying";
/// The policy reserved the point to a person: a human rules.
pub const POINT_ESCALATED: &str = "escalated";
/// Decided.
pub const POINT_DECIDED: &str = "decided";

/// Question statuses.
pub const QUESTION_OPEN: &str = "open";
pub const QUESTION_ANSWERED: &str = "answered";

/// DecisionEvent is the payload of a KindDecisionPoint item.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct DecisionEvent {
    pub op: String,
    /// Point is the decision point: the id of the item that opened it (empty on open).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub point: String,
    // open
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub options: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub criteria: Vec<String>,
    /// Policy is the opaque policy values of the point, as the DecisionPolicy resolved them at the opening: the graph
    /// stores them and hands them back to the policy, never reads them.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub policy: Map<String, Value>,
    // rule
    #[serde(skip_serializing_if = "String::is_empty")]
    pub outcome: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub option: String,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub confidence: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub justification: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub questions: Vec<String>,
    /// Human tells a ruling or a ratification comes from a person, not an agent. The confidence of a ruling is
    /// recorded as given, the policy reads it.
    #[serde(skip_serializing_if = "is_false")]
    pub human: bool,
    /// answer: the question and its answer; the process that investigated it, if any
    #[serde(rename = "questionId", skip_serializing_if = "String::is_empty")]
    pub question_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub answer: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    // ratify
    #[serde(skip_serializing_if = "is_false")]
    pub accept: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
}

pub(crate) fn validate_decision_event(e: Option<&DecisionEvent>) -> Result<(), Error> {
    let Some(e) = e else {
        return Err(Error::invalid("decision point item requires decisionEvent"));
    };
    match e.op.as_str() {
        DECISION_OPEN_OP => {
            if e.question.is_empty() {
                return Err(Error::invalid("a decision point needs a question"));
            }
        }
        DECISION_RULE_OP | DECISION_RATIFY_OP => {
            if e.point.is_empty() {
                return Err(Error::invalid(format!(
                    "decision {} requires a point",
                    e.op
                )));
            }
        }
        DECISION_ANSWER_OP => {
            if e.point.is_empty() || e.question_id.is_empty() {
                return Err(Error::invalid("an answer requires a point and a question"));
            }
        }
        op => return Err(Error::invalid(format!("unknown decision op {op:?}"))),
    }
    Ok(())
}

/// Question is a question raised by an undecidable ruling: it blocks its decision point until answered.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Question {
    pub id: String,
    pub point: String,
    pub text: String,
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub answer: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub answered_by: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub asked_at: DateTime<Utc>,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for Question {
    fn default() -> Self {
        Question {
            id: Default::default(),
            point: Default::default(),
            text: Default::default(),
            status: Default::default(),
            answer: Default::default(),
            answered_by: Default::default(),
            process: Default::default(),
            asked_at: gotime::zero(),
        }
    }
}

/// Ruling is the last ruling of a decision point.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Ruling {
    pub outcome: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub option: String,
    #[serde(skip_serializing_if = "is_zero_f64")]
    pub confidence: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub justification: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    #[serde(skip_serializing_if = "is_false")]
    pub human: bool,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub at: DateTime<Utc>,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for Ruling {
    fn default() -> Self {
        Ruling {
            outcome: Default::default(),
            option: Default::default(),
            confidence: Default::default(),
            justification: Default::default(),
            by: Default::default(),
            human: Default::default(),
            at: gotime::zero(),
        }
    }
}

/// DecisionPoint is a decision point, replayed from its events.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct DecisionPoint {
    pub id: String,
    pub question: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub options: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub criteria: Vec<String>,
    /// Policy is the policy values of the point (see DecisionEvent.policy), updated by the policy as it folds the
    /// events (a count of rounds, say): a private copy, replayed with the point.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub policy: Map<String, Value>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub opened_at: DateTime<Utc>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub opened_by: String,
    pub status: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub questions: Vec<Question>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ruling: Option<Ruling>,
    /// HumanOnly tells only a person may rule the point now: by design, or because it is escalated.
    #[serde(skip_serializing_if = "is_false")]
    pub human_only: bool,
    /// Escalation says why the policy reserved the point to a person ("" = not escalated).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub escalation: String,
    /// Option and DecidedAt / DecidedBy are set once decided (DecidedAt None: Go's zero time).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub option: String,
    #[serde(with = "gotime::option")]
    pub decided_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub decided_by: String,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for DecisionPoint {
    fn default() -> Self {
        DecisionPoint {
            id: Default::default(),
            question: Default::default(),
            options: Default::default(),
            criteria: Default::default(),
            policy: Default::default(),
            opened_at: gotime::zero(),
            opened_by: Default::default(),
            status: Default::default(),
            questions: Default::default(),
            ruling: Default::default(),
            human_only: Default::default(),
            escalation: Default::default(),
            option: Default::default(),
            decided_at: Default::default(),
            decided_by: Default::default(),
        }
    }
}

impl DecisionPoint {
    /// OpenQuestions counts the questions waiting for an answer.
    pub fn open_questions(&self) -> usize {
        self.questions
            .iter()
            .filter(|q| q.status == QUESTION_OPEN)
            .count()
    }

    /// Pending reports whether the point is not decided yet.
    pub fn pending(&self) -> bool {
        self.status != POINT_DECIDED
    }

    /// NeedsHuman reports whether only a person may rule the point: reserved to one by design, or escalated.
    pub fn needs_human(&self) -> bool {
        self.human_only || !self.escalation.is_empty()
    }

    fn decide(&mut self, r: Ruling) {
        self.option = r.option.clone();
        self.decided_at = Some(r.at).filter(|t| !gotime::is_zero(t));
        self.decided_by = r.by.clone();
        self.ruling = Some(r);
    }

    /// settle derives the status of a replayed point.
    fn settle(&mut self, now: DateTime<Utc>, policy: &dyn DecisionPolicy) {
        if self.decided_at.is_some() {
            self.status = POINT_DECIDED.into();
            return;
        }
        let (human_only, escalation) = policy.reserve(self, now);
        self.human_only = human_only || !escalation.is_empty();
        self.escalation = escalation;
        self.status = if self
            .ruling
            .as_ref()
            .is_some_and(|r| r.outcome == OUTCOME_DECIDED)
        {
            POINT_RATIFYING
        } else if self.open_questions() > 0 {
            POINT_BLOCKED
        } else if !self.escalation.is_empty() {
            POINT_ESCALATED
        } else {
            POINT_OPEN
        }
        .into();
    }
}

/// DecisionPolicy is the rule that says when a ruling settles a decision point and when only a person may rule it.
/// It is pure and deterministic: decision_points_at replays the log with it, at any time, and must find the same
/// points. The graph core has no policy of its own beyond default_decision_policy; pkg/decision is the confidence /
/// rounds / deadline policy of the platform (ADR 0067).
pub trait DecisionPolicy: Send + Sync {
    /// Open resolves the policy values the opener gave: defaults, validation, values relative to now (a deadline).
    /// The result is stored on the opening event as DecisionPoint.policy; an error refuses the opening.
    fn open(
        &self,
        given: &Map<String, Value>,
        now: DateTime<Utc>,
    ) -> Result<Map<String, Value>, Error>;
    /// Fold takes an event of a point into account, after the core recorded it, and says whether it settles the
    /// point: a rule (decided or undecidable) or a ratify (accepted or refused). It may update p.policy, which is a
    /// private copy.
    fn fold(&self, p: &mut DecisionPoint, ev: &DecisionEvent) -> bool;
    /// Reserve says whether only a person may rule the point at now (humanOnly), and why when the point is
    /// escalated. It is asked of the points that are not decided.
    fn reserve(&self, p: &DecisionPoint, now: DateTime<Utc>) -> (bool, String);
}

/// DefaultDecisionPolicy is the policy of a graph that was given none: a decided ruling of anyone settles the point,
/// an accepted ratification settles it, nothing is ever reserved to a person.
pub fn default_decision_policy() -> &'static dyn DecisionPolicy {
    &MinimalPolicy
}

struct MinimalPolicy;

impl DecisionPolicy for MinimalPolicy {
    fn open(
        &self,
        given: &Map<String, Value>,
        _: DateTime<Utc>,
    ) -> Result<Map<String, Value>, Error> {
        if !given.is_empty() {
            return Err(Error::invalid(format!(
                "the graph has no decision policy: no policy values ({} given)",
                given.len()
            )));
        }
        Ok(Map::new())
    }

    fn fold(&self, _: &mut DecisionPoint, ev: &DecisionEvent) -> bool {
        match ev.op.as_str() {
            DECISION_RULE_OP => ev.outcome == OUTCOME_DECIDED,
            DECISION_RATIFY_OP => ev.accept,
            _ => false,
        }
    }

    fn reserve(&self, _: &DecisionPoint, _: DateTime<Utc>) -> (bool, String) {
        (false, String::new())
    }
}

/// QuestionID names the i-th question raised by the ruling item rule.
pub fn question_id(rule: &ItemID, i: usize) -> String {
    format!("{rule}:{}", i + 1)
}

impl Change {
    /// DecisionPointsAt replays the decision points of the change, oldest first, at a moment (the policy judges its
    /// deadlines against it), under a policy (None: default_decision_policy). The policy is a parameter, not a
    /// registry: the replay stays a pure function of the log, the moment and the policy. Only the events of the main
    /// flow count.
    pub fn decision_points_at(
        &self,
        now: DateTime<Utc>,
        policy: Option<&dyn DecisionPolicy>,
    ) -> Vec<DecisionPoint> {
        let policy: &dyn DecisionPolicy = match policy {
            Some(p) => p,
            None => default_decision_policy(),
        };
        let mut out: Vec<DecisionPoint> = Vec::new();
        let mut at: HashMap<String, usize> = HashMap::new();
        for it in &self.items {
            let Some(e) = it.decision_event.as_ref() else {
                continue;
            };
            if it.r#type != TYPE_DECISION_POINT || !it.flow.is_empty() {
                continue;
            }
            if e.op == DECISION_OPEN_OP {
                let d = DecisionPoint {
                    id: it.id.to_string(),
                    question: e.question.clone(),
                    options: e.options.clone(),
                    criteria: e.criteria.clone(),
                    policy: e.policy.clone(),
                    opened_at: it.created_at,
                    opened_by: e.by.clone(),
                    ..Default::default()
                };
                at.insert(d.id.clone(), out.len());
                out.push(d);
                continue;
            }
            let Some(&i) = at.get(&e.point) else { continue };
            let d = &mut out[i];
            if !d.decided_by.is_empty() || d.decided_at.is_some() {
                continue; // a decided point does not move
            }
            match e.op.as_str() {
                DECISION_RULE_OP => {
                    let r = Ruling {
                        outcome: e.outcome.clone(),
                        option: e.option.clone(),
                        confidence: e.confidence,
                        justification: e.justification.clone(),
                        by: e.by.clone(),
                        human: e.human,
                        at: it.created_at,
                    };
                    d.ruling = Some(r.clone());
                    let settled = policy.fold(d, e);
                    if e.outcome == OUTCOME_UNDECIDABLE {
                        for (j, q) in e.questions.iter().enumerate() {
                            d.questions.push(Question {
                                id: question_id(&it.id, j),
                                point: d.id.clone(),
                                text: q.clone(),
                                status: QUESTION_OPEN.into(),
                                asked_at: it.created_at,
                                ..Default::default()
                            });
                        }
                    } else if settled {
                        d.decide(r);
                    }
                }
                DECISION_RATIFY_OP => {
                    let Some(ruling) = d.ruling.clone().filter(|r| r.outcome == OUTCOME_DECIDED)
                    else {
                        continue;
                    };
                    if policy.fold(d, e) {
                        d.decide(Ruling {
                            outcome: OUTCOME_DECIDED.into(),
                            option: ruling.option,
                            confidence: ruling.confidence,
                            justification: ruling.justification,
                            by: e.by.clone(),
                            human: true,
                            at: it.created_at,
                        });
                    } else {
                        d.ruling = None;
                    }
                }
                DECISION_ANSWER_OP => {
                    for q in d.questions.iter_mut() {
                        if q.id == e.question_id && q.status == QUESTION_OPEN {
                            q.status = QUESTION_ANSWERED.into();
                            q.answer = e.answer.clone();
                            q.answered_by = e.by.clone();
                            q.process = e.process.clone();
                        }
                    }
                }
                _ => {}
            }
        }
        for d in out.iter_mut() {
            d.settle(now, policy);
        }
        out
    }

    /// DecisionPoint returns a decision point replayed at now under a policy (None: default_decision_policy).
    pub fn decision_point(
        &self,
        id: &str,
        now: DateTime<Utc>,
        policy: Option<&dyn DecisionPolicy>,
    ) -> Option<DecisionPoint> {
        self.decision_points_at(now, policy)
            .into_iter()
            .find(|d| d.id == id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{ChangeItem, POINT_BLOCKED};
    use chrono::{Duration, TimeZone};
    use serde_json::json;

    fn t0() -> DateTime<Utc> {
        Utc.with_ymd_and_hms(2026, 9, 1, 0, 0, 0).unwrap()
    }

    fn add(c: &mut Change, id: &str, e: DecisionEvent) {
        let n = c.items.len() as i64;
        c.items.push(ChangeItem {
            id: id.into(),
            r#type: TYPE_DECISION_POINT,
            decision_event: Some(e),
            created_at: t0() + Duration::minutes(n),
            ..Default::default()
        });
    }

    fn ev(op: &str) -> DecisionEvent {
        DecisionEvent {
            op: op.into(),
            ..Default::default()
        }
    }

    // The mechanism of a decision point under the default policy (a decided ruling of anyone settles it, nothing is
    // reserved to a person): the policy-bearing behaviour is tested in pkg/decision.
    #[test]
    fn decision_point_replay() {
        let mut c = Change::default();
        let point = |c: &Change| {
            c.decision_point("P", t0() + Duration::hours(1), None)
                .expect("no point")
        };
        add(
            &mut c,
            "P",
            DecisionEvent {
                question: "which PSP?".into(),
                options: vec!["a".into(), "b".into()],
                ..ev(DECISION_OPEN_OP)
            },
        );
        let d = point(&c);
        assert!(
            d.status == POINT_OPEN && !d.needs_human() && d.escalation.is_empty(),
            "opened: {d:?}"
        );
        // undecidable: two questions block the point
        add(
            &mut c,
            "R1",
            DecisionEvent {
                point: "P".into(),
                outcome: OUTCOME_UNDECIDABLE.into(),
                justification: "costs unknown".into(),
                questions: vec!["fees of a?".into(), "fees of b?".into()],
                ..ev(DECISION_RULE_OP)
            },
        );
        let d = point(&c);
        assert!(
            d.status == POINT_BLOCKED && d.open_questions() == 2 && d.questions[0].id == "R1:1",
            "blocked: {d:?}"
        );
        add(
            &mut c,
            "A1",
            DecisionEvent {
                point: "P".into(),
                question_id: "R1:1".into(),
                answer: "1%".into(),
                ..ev(DECISION_ANSWER_OP)
            },
        );
        add(
            &mut c,
            "A2",
            DecisionEvent {
                point: "P".into(),
                question_id: "R1:2".into(),
                answer: "2%".into(),
                ..ev(DECISION_ANSWER_OP)
            },
        );
        let d = point(&c);
        assert!(
            d.status == POINT_OPEN && d.open_questions() == 0,
            "answered: {d:?}"
        );
        // whoever rules decided settles the point, whatever the confidence
        add(
            &mut c,
            "R2",
            DecisionEvent {
                point: "P".into(),
                outcome: OUTCOME_DECIDED.into(),
                option: "a".into(),
                confidence: 0.1,
                by: "agent".into(),
                ..ev(DECISION_RULE_OP)
            },
        );
        let d = point(&c);
        assert!(
            d.status == POINT_DECIDED && d.option == "a" && d.decided_by == "agent",
            "decided: {d:?}"
        );
        // a decided point does not move any more
        add(
            &mut c,
            "R3",
            DecisionEvent {
                point: "P".into(),
                outcome: OUTCOME_DECIDED.into(),
                option: "b".into(),
                human: true,
                ..ev(DECISION_RULE_OP)
            },
        );
        add(
            &mut c,
            "X",
            DecisionEvent {
                point: "P".into(),
                accept: true,
                ..ev(DECISION_RATIFY_OP)
            },
        );
        let d = point(&c);
        assert!(d.option == "a" && d.decided_by == "agent", "moved: {d:?}");
        // no deadline, no rounds under the default policy
        add(
            &mut c,
            "Q",
            DecisionEvent {
                question: "when?".into(),
                ..ev(DECISION_OPEN_OP)
            },
        );
        for i in 0..5 {
            add(
                &mut c,
                &format!("U{i}"),
                DecisionEvent {
                    point: "Q".into(),
                    outcome: OUTCOME_UNDECIDABLE.into(),
                    justification: "?".into(),
                    questions: vec!["q".into()],
                    ..ev(DECISION_RULE_OP)
                },
            );
        }
        let d = c
            .decision_point("Q", t0() + Duration::hours(1000), None)
            .unwrap_or_default();
        assert!(
            d.status == POINT_BLOCKED && d.escalation.is_empty() && !d.needs_human(),
            "no rounds, no deadline: {d:?}"
        );
        // the default policy takes no policy values, the opener is told
        let given = json!({"threshold": 0.5}).as_object().unwrap().clone();
        assert!(
            default_decision_policy().open(&given, t0()).is_err(),
            "the default policy accepted policy values"
        );
    }

    // reserving is a policy that holds every ruling for ratification, counts in the point and reserves the point to
    // a person once it has two failed rounds or a deadline passed: the replay only does what the policy says.
    struct Reserving;

    impl DecisionPolicy for Reserving {
        fn open(
            &self,
            given: &Map<String, Value>,
            _: DateTime<Utc>,
        ) -> Result<Map<String, Value>, Error> {
            Ok(given.clone())
        }

        fn fold(&self, p: &mut DecisionPoint, ev: &DecisionEvent) -> bool {
            let n = p.policy.get("seen").and_then(Value::as_i64).unwrap_or(0);
            p.policy.insert("seen".into(), json!(n + 1));
            ev.op == DECISION_RATIFY_OP && ev.accept
        }

        fn reserve(&self, p: &DecisionPoint, _: DateTime<Utc>) -> (bool, String) {
            if p.policy.get("seen").and_then(Value::as_i64).unwrap_or(0) >= 2 {
                return (true, "seen twice".into());
            }
            (
                p.policy.get("human") == Some(&Value::Bool(true)),
                String::new(),
            )
        }
    }

    #[test]
    fn decision_point_replay_asks_the_policy() {
        let mut c = Change::default();
        let point = |c: &Change, id: &str| {
            c.decision_point(id, t0() + Duration::hours(1), Some(&Reserving))
                .expect("no point")
        };
        let given = json!({"human": true}).as_object().unwrap().clone();
        add(
            &mut c,
            "P",
            DecisionEvent {
                question: "go?".into(),
                policy: given.clone(),
                ..ev(DECISION_OPEN_OP)
            },
        );
        let d = point(&c, "P");
        assert!(
            d.status == POINT_OPEN
                && d.human_only
                && d.needs_human()
                && d.escalation.is_empty()
                && d.policy["human"] == true,
            "reserved by design: {d:?}"
        );
        add(
            &mut c,
            "R1",
            DecisionEvent {
                point: "P".into(),
                outcome: OUTCOME_DECIDED.into(),
                option: "a".into(),
                confidence: 1.0,
                ..ev(DECISION_RULE_OP)
            },
        );
        let d = point(&c, "P");
        assert!(
            d.status == POINT_RATIFYING && d.ruling.is_some() && d.decided_by.is_empty(),
            "the policy did not settle the ruling: {d:?}"
        );
        add(
            &mut c,
            "X",
            DecisionEvent {
                point: "P".into(),
                accept: false,
                ..ev(DECISION_RATIFY_OP)
            },
        );
        let d = point(&c, "P");
        assert!(
            d.status == POINT_ESCALATED && d.escalation == "seen twice" && d.ruling.is_none(),
            "a refused ratification: {d:?}"
        );
        add(
            &mut c,
            "X2",
            DecisionEvent {
                point: "P".into(),
                outcome: OUTCOME_DECIDED.into(),
                option: "a".into(),
                human: true,
                by: "ann".into(),
                ..ev(DECISION_RULE_OP)
            },
        );
        add(
            &mut c,
            "X3",
            DecisionEvent {
                point: "P".into(),
                accept: true,
                by: "ann".into(),
                ..ev(DECISION_RATIFY_OP)
            },
        );
        let d = point(&c, "P");
        assert!(
            d.status == POINT_DECIDED
                && d.option == "a"
                && d.decided_by == "ann"
                && d.ruling.as_ref().unwrap().human,
            "ratified: {d:?}"
        );
        // the point holds its own copy of the values: the policy state never reaches the log
        assert!(
            !given.contains_key("seen"),
            "the policy state leaked into the event: {given:?}"
        );
        let ev0 = c.items[0].decision_event.as_ref().unwrap();
        assert!(
            !ev0.policy.contains_key("seen"),
            "the policy state leaked into the event: {:?}",
            ev0.policy
        );
        // the replay is a function of the log: the policy state does not leak from one replay to the next
        let (a, b) = (point(&c, "P"), point(&c, "P"));
        assert_eq!(a.policy.get("seen"), b.policy.get("seen"), "replays differ");
    }
}
