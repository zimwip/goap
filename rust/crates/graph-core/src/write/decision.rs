//! The decision points of a change (Go's `graph/graph/decision.go`, ADR 0009 §4): opened, ruled, their questions
//! answered, a ruling the policy did not settle ratified. Every operation is a `decision_point` item of the main flow;
//! the points are its replay ([`crate::decision::points_at`]). A change working on options (flows) is the Go service's:
//! a decided point that names an option selects it.
use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};

use super::tx::Tx;
use crate::{
    GraphError, Result,
    change::Change,
    decision::{
        self, DecisionEvent, DecisionPoint, OP_ANSWER, OP_OPEN, OP_RATIFY, OP_RULE, OUTCOME_DECIDED,
        OUTCOME_UNDECIDABLE, POINT_BLOCKED, POINT_RATIFYING, PointItem, QUESTION_OPEN, TYPE_DECISION_POINT,
    },
    gojson,
    impact::{LOG_FACT, LogEntry},
    model::ChangeId,
    store::{ReadTx, WriteTx},
};

/// A decision point item as Go marshals a `domain.ChangeItem` carrying a decision event.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct PointItemJson<'a> {
    id: &'a str,
    #[serde(rename = "type")]
    typ: &'a str,
    target: &'a str,
    status: &'a str,
    decision_event: &'a DecisionEvent,
    produced_by: &'a str,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

/// The fields of a point item the replay reads.
#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase")]
struct ReadItem {
    id: String,
    #[serde(default)]
    flow: String,
    #[serde(default)]
    decision_event: Option<DecisionEvent>,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

/// The decision points of a change, replayed at a moment from its log.
pub async fn points_of<T: ReadTx>(tx: &T, id: &ChangeId, now: DateTime<Utc>) -> Result<Vec<DecisionPoint>> {
    let mut items = Vec::new();
    for l in tx.log(id, 0, &[&format!("{LOG_FACT}.{TYPE_DECISION_POINT}")]).await? {
        if l.typ != format!("{LOG_FACT}.{TYPE_DECISION_POINT}") {
            continue;
        }
        let it: ReadItem = serde_json::from_str(&l.payload).map_err(GraphError::internal)?;
        if let Some(event) = it.decision_event {
            items.push(PointItem {
                id: it.id,
                flow: it.flow,
                created_at: it.created_at,
                event,
            });
        }
    }
    Ok(decision::points_at(&items, now))
}

/// What opens a point (Go's `OpenDecisionRequest`): `options` none for every open option of the change.
#[derive(Debug, Clone, Default)]
pub struct OpenDecision {
    pub question: String,
    pub options: Option<Vec<String>>,
    pub criteria: Vec<String>,
    pub policy: Map<String, Value>,
    pub choices: Vec<String>,
    pub by: String,
}

/// A ruling (Go's `RuleRequest`).
#[derive(Debug, Clone, Default)]
pub struct Rule {
    pub point: String,
    pub outcome: String,
    pub option: String,
    pub confidence: f64,
    pub justification: String,
    pub questions: Vec<String>,
    pub human: bool,
    pub by: String,
    pub role: String,
}

impl<S: WriteTx> Tx<'_, S> {
    /// The change a decision works on (Go's `flowChange`): open, and with no option (the Go service's).
    async fn decision_change(&mut self, id: &ChangeId) -> Result<Change> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if c.status != "draft" && c.status != "active" {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        let flows = self
            .store
            .log(id, 0, &[&format!("{LOG_FACT}.flow"), &format!("{LOG_FACT}.decision")])
            .await?;
        if flows
            .iter()
            .any(|l| l.typ == format!("{LOG_FACT}.flow") || l.typ == format!("{LOG_FACT}.decision"))
        {
            return Err(GraphError::unimplemented(
                "a decision of a change working on options (ADR 0032): the Go service's",
            ));
        }
        Ok(c)
    }

    /// Appends an event of a point to the log of a change, on the main flow; answers the id of its item.
    async fn put_decision_event(&mut self, id: &ChangeId, e: &DecisionEvent) -> Result<String> {
        // what `ChangeItem.Validate` asks of the event
        let problem = match e.op.as_str() {
            OP_OPEN if e.question.is_empty() => Some("a decision point needs a question".to_string()),
            OP_RULE | OP_RATIFY if e.point.is_empty() => Some(format!("decision {} requires a point", e.op)),
            OP_ANSWER if e.point.is_empty() || e.question_id.is_empty() => {
                Some("an answer requires a point and a question".to_string())
            }
            _ => None,
        };
        if let Some(p) = problem {
            return Err(GraphError::invalid(p));
        }
        let item = self.env.new_id();
        let at = self.env.now();
        let target = format!("decision.{}", e.op);
        let by = if e.by.is_empty() {
            "graph.decision"
        } else {
            e.by.as_str()
        };
        let payload = gojson::to_string(&PointItemJson {
            id: &item,
            typ: TYPE_DECISION_POINT,
            target: &target,
            status: "accepted",
            decision_event: e,
            produced_by: by,
            created_at: at,
        });
        self.append_log(&LogEntry {
            seq: 0,
            id: item.clone(),
            change: id.clone(),
            typ: format!("{LOG_FACT}.{TYPE_DECISION_POINT}"),
            flow: String::new(),
            process: String::new(),
            execution: String::new(),
            subject: target,
            by: by.to_string(),
            at,
            causation: String::new(),
            payload,
        })
        .await?;
        Ok(item)
    }

    async fn points(&self, id: &ChangeId) -> Result<Vec<DecisionPoint>> {
        points_of(self.store, id, self.env.now()).await
    }

    async fn point(&self, id: &ChangeId, point: &str) -> Result<DecisionPoint> {
        self.points(id)
            .await?
            .into_iter()
            .find(|d| d.id == point)
            .ok_or_else(|| GraphError::not_found(format!("decision point {point} of change {id}")))
    }

    /// The pending point a ruling or a ratification names, else the only pending one (Go's `pendingPoint`).
    async fn pending_point(&self, id: &ChangeId, point: &str) -> Result<DecisionPoint> {
        let points = self.points(id).await?;
        if point.is_empty() {
            let mut pending: Vec<DecisionPoint> = points.into_iter().filter(DecisionPoint::pending).collect();
            if pending.len() != 1 {
                return Err(GraphError::invalid(format!(
                    "change {id} has {} pending decision points: name one",
                    pending.len()
                )));
            }
            return Ok(pending.remove(0));
        }
        let d = points
            .into_iter()
            .find(|d| d.id == point)
            .ok_or_else(|| GraphError::not_found(format!("decision point {point} of change {id}")))?;
        if !d.pending() {
            return Err(GraphError::conflict(format!("decision point {point} is decided")));
        }
        Ok(d)
    }

    /// Opens a decision point on a change.
    pub async fn open_decision(&mut self, id: &ChangeId, mut r#in: OpenDecision) -> Result<DecisionPoint> {
        let q = r#in.question.trim().to_string();
        if q.is_empty() {
            return Err(GraphError::invalid("a decision point needs a question"));
        }
        let policy = decision::open_policy(&r#in.policy, self.env.now())
            .map_err(|e| GraphError::invalid(format!("decision policy: {e}")))?;
        let mut c = self.decision_change(id).await?;
        // a change with no option: every open option is none, a named one is not an option of the change
        let mut options = r#in.options.take().unwrap_or_default();
        if let Some(o) = options.first() {
            return Err(GraphError::not_found(format!("option {o} of change {id}")));
        }
        for ch in &r#in.choices {
            if ch.is_empty() || options.contains(ch) {
                return Err(GraphError::invalid(format!("choice {ch:?}: a choice is named once")));
            }
            options.push(ch.clone());
        }
        let e = DecisionEvent {
            op: OP_OPEN.into(),
            question: q,
            options,
            criteria: r#in.criteria,
            policy,
            by: r#in.by,
            ..DecisionEvent::default()
        };
        let pid = self.put_decision_event(id, &e).await?;
        if c.status == "draft" {
            c.status = "active".into();
            self.put_change(&c).await?;
        }
        self.point(id, &pid).await
    }

    /// Records a ruling: decided and settled by the policy, it decides the point; decided and not settled, it waits for
    /// a ratification; undecidable, its questions block the point until they are answered.
    pub async fn rule_decision(&mut self, id: &ChangeId, mut r#in: Rule) -> Result<DecisionPoint> {
        r#in.justification = r#in.justification.trim().to_string();
        self.decision_change(id).await?;
        let cur = self.pending_point(id, &r#in.point).await?;
        if cur.status == POINT_BLOCKED {
            return Err(GraphError::conflict(format!(
                "decision point {} has {} open question(s): answer them first",
                cur.id,
                cur.open_questions()
            )));
        }
        if cur.needs_human() && !r#in.human {
            let why = if cur.escalation.is_empty() {
                "it is reserved to a person".to_string()
            } else {
                format!("it is escalated ({})", cur.escalation)
            };
            return Err(GraphError::conflict(format!(
                "decision point {} is ruled by a person: {why}",
                cur.id
            )));
        }
        if cur.status == POINT_RATIFYING && !r#in.human {
            return Err(GraphError::conflict(format!(
                "decision point {} waits for the ratification of its ruling",
                cur.id
            )));
        }
        // the seat of a ruling of a board (ADR 0132 §3); a platform service sits in any (`graphsvc.SeatAuthorizer`)
        let seats = decision::board(&cur);
        if seats.is_empty() {
            if !r#in.role.is_empty() {
                return Err(GraphError::invalid(format!(
                    "decision point {} has no board: rule it without a seat",
                    cur.id
                )));
            }
        } else if !r#in.human {
            return Err(GraphError::conflict(format!(
                "decision point {} is ruled by its board ({}): people only",
                cur.id,
                seats.join(", ")
            )));
        } else if r#in.role.is_empty() {
            return Err(GraphError::invalid(format!(
                "decision point {} is ruled by its board: name your seat ({})",
                cur.id,
                seats.join(", ")
            )));
        }
        let mut e = DecisionEvent {
            op: OP_RULE.into(),
            point: cur.id.clone(),
            outcome: r#in.outcome.clone(),
            justification: r#in.justification.clone(),
            human: r#in.human,
            by: r#in.by.clone(),
            role: r#in.role.clone(),
            ..DecisionEvent::default()
        };
        match r#in.outcome.as_str() {
            OUTCOME_DECIDED => {
                if !cur.options.is_empty() && !cur.options.contains(&r#in.option) {
                    return Err(GraphError::invalid(format!(
                        "decision point {} chooses among [{}], not {:?}",
                        cur.id,
                        cur.options.join(" "),
                        r#in.option
                    )));
                }
                if !(0.0..=1.0).contains(&r#in.confidence) {
                    return Err(GraphError::invalid(format!(
                        "a confidence is between 0 and 1, not {}",
                        gojson::float(r#in.confidence)
                    )));
                }
                if r#in.justification.is_empty() {
                    return Err(GraphError::invalid("a decision needs a justification"));
                }
                e.option = r#in.option;
                e.confidence = r#in.confidence;
            }
            OUTCOME_UNDECIDABLE => {
                e.questions = r#in
                    .questions
                    .iter()
                    .map(|q| q.trim().to_string())
                    .filter(|q| !q.is_empty())
                    .collect();
                if e.questions.is_empty() || r#in.justification.is_empty() {
                    return Err(GraphError::invalid(
                        "an undecidable ruling says why and asks at least one question",
                    ));
                }
            }
            o => {
                return Err(GraphError::invalid(format!(
                    "a ruling is {OUTCOME_DECIDED} or {OUTCOME_UNDECIDABLE}, not {o:?}"
                )));
            }
        }
        self.put_decision_event(id, &e).await?;
        // a decided point naming an option would select it: a change with no option has none to select
        self.point(id, &cur.id).await
    }

    /// Answers an open question of a decision point; `process` is the one that investigated it, if any.
    pub async fn answer_question(
        &mut self,
        id: &ChangeId,
        question: &str,
        answer: &str,
        process: &str,
        by: &str,
    ) -> Result<DecisionPoint> {
        let answer = answer.trim();
        if answer.is_empty() {
            return Err(GraphError::invalid("an answer needs a text"));
        }
        self.decision_change(id).await?;
        let mut point = String::new();
        for p in self.points(id).await? {
            for q in p.questions.iter().filter(|q| q.id == question) {
                if q.status != QUESTION_OPEN {
                    return Err(GraphError::conflict(format!("question {question} is {}", q.status)));
                }
                point = p.id.clone();
            }
        }
        if point.is_empty() {
            return Err(GraphError::not_found(format!("question {question} of change {id}")));
        }
        let e = DecisionEvent {
            op: OP_ANSWER.into(),
            point: point.clone(),
            question_id: question.into(),
            answer: answer.into(),
            process: process.into(),
            by: by.into(),
            ..DecisionEvent::default()
        };
        self.put_decision_event(id, &e).await?;
        self.point(id, &point).await
    }

    /// Accepts or refuses the ruling the policy did not settle (a person's act): accepted, it decides the point;
    /// refused, the point is open again.
    pub async fn ratify_decision(
        &mut self,
        id: &ChangeId,
        point: &str,
        accept: bool,
        by: &str,
        comment: &str,
    ) -> Result<DecisionPoint> {
        self.decision_change(id).await?;
        let cur = self.pending_point(id, point).await?;
        if cur.status != POINT_RATIFYING {
            return Err(GraphError::conflict(format!(
                "decision point {point} is {}: nothing to ratify",
                cur.status
            )));
        }
        if !accept && comment.trim().is_empty() {
            return Err(GraphError::invalid("refusing a ruling needs a comment"));
        }
        let e = DecisionEvent {
            op: OP_RATIFY.into(),
            point: point.into(),
            accept,
            comment: comment.into(),
            human: true,
            by: by.into(),
            ..DecisionEvent::default()
        };
        self.put_decision_event(id, &e).await?;
        self.point(id, point).await
    }
}
