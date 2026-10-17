//! Port of `decision.go`. Decision points of a change (ADR 0009 §4): opened on the question to settle, ruled (decided,
//! or undecidable with the questions that block it), the questions answered, a ruling that did not settle the point
//! ratified by a person. A decided point that names an option selects it (ADR 0032 §6). Every operation is a
//! decision point fact of the main flow; the state is its replay (Change::decision_points_at). When a ruling settles a
//! point and when only a person may rule it is the policy of Graph::decision_policy (ADR 0067), not the graph's.

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, ChangeItem, DECISION_ANSWER_OP,
    DECISION_OPEN_OP, DECISION_RATIFY_OP, DECISION_RULE_OP, DecisionEvent, DecisionPoint,
    DecisionPolicy, FLOW_OPEN, ITEM_ACCEPTED, ItemID, OUTCOME_DECIDED, OUTCOME_UNDECIDABLE,
    POINT_BLOCKED, POINT_DECIDED, POINT_RATIFYING, QUESTION_OPEN, TYPE_DECISION_POINT,
    default_decision_policy,
};

use crate::api::{OpenDecisionRequest, RuleRequest};
use crate::changelog::put_item;
use crate::flow::{first_non_empty, flow_change};
use crate::lifecycle::invalidf;
use crate::option::open_option;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// decisionPolicy is the policy of the graph, the minimal one when none was given.
    pub(crate) fn decision_policy_of(&self) -> &dyn DecisionPolicy {
        match &self.decision_policy {
            Some(p) => p.as_ref(),
            None => default_decision_policy(),
        }
    }

    /// decisionEvent appends a decision event to the log of a change, on the main flow.
    pub(crate) async fn decision_event(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        e: DecisionEvent,
    ) -> Result<()> {
        self.put_decision_event(ctx, tx, id, e).await.map(|_| ())
    }

    pub(crate) async fn put_decision_event(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        e: DecisionEvent,
    ) -> Result<ItemID> {
        let it = ChangeItem {
            id: ItemID::new(self.new_id()),
            r#type: TYPE_DECISION_POINT,
            target: format!("decision.{}", e.op),
            status: ITEM_ACCEPTED,
            produced_by: first_non_empty(&[&e.by, "graph.decision"]),
            decision_event: Some(e),
            created_at: self.now(),
            ..Default::default()
        };
        self.check_item(&it).map_err(invalidf)?;
        put_item(ctx, tx, id, &it).await?;
        Ok(it.id)
    }

    /// OpenDecision opens a decision point on a change.
    pub async fn open_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: OpenDecisionRequest,
    ) -> Result<DecisionPoint> {
        let q = input.question.trim().to_string();
        if q.is_empty() {
            return Err(invalidf("a decision point needs a question"));
        }
        let policy = self
            .decision_policy_of()
            .open(&input.policy, self.now())
            .map_err(|e| invalidf(format!("decision policy: {e}")))?;
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = flow_change(ctx, &mut tx, id).await?;
        let mut options = match &input.options {
            Some(o) => o.clone(),
            None => c
                .options()
                .into_iter()
                .filter(|o| o.status == FLOW_OPEN)
                .map(|o| o.id)
                .collect(),
        };
        for o in &mut options {
            *o = option_id(&c, o);
            open_option(&c, o)?;
        }
        let e = DecisionEvent {
            op: DECISION_OPEN_OP.into(),
            question: q,
            options,
            criteria: input.criteria.clone(),
            policy,
            by: input.by.clone(),
            ..Default::default()
        };
        let pid = self.put_decision_event(ctx, &mut tx, id, e).await?;
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
            tx.put_change(ctx, c).await?;
        }
        let d = self.decision_of(ctx, &mut tx, id, pid.as_str()).await?;
        tx.commit(ctx).await?;
        Ok(d)
    }

    /// RuleDecision records a ruling. A decided ruling that the policy settles decides the point and selects its
    /// option; otherwise it waits for a ratification. An undecidable ruling raises its questions: the point is blocked
    /// until they are answered.
    pub async fn rule_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        mut input: RuleRequest,
    ) -> Result<DecisionPoint> {
        input.justification = input.justification.trim().to_string();
        let mut tx = self.change_tx(ctx, id).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        let cur = pending_point(
            &c,
            &input.point,
            self.now(),
            self.decision_policy.as_deref(),
        )?;
        if cur.status == POINT_BLOCKED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "decision point {} has {} open question(s): answer them first",
                    cur.id,
                    cur.open_questions()
                ),
            ));
        }
        if cur.needs_human() && !input.human {
            let why = if cur.escalation.is_empty() {
                "it is reserved to a person".to_string()
            } else {
                format!("it is escalated ({})", cur.escalation)
            };
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("decision point {} is ruled by a person: {why}", cur.id),
            ));
        }
        if cur.status == POINT_RATIFYING && !input.human {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "decision point {} waits for the ratification of its ruling",
                    cur.id
                ),
            ));
        }
        let mut e = DecisionEvent {
            op: DECISION_RULE_OP.into(),
            point: cur.id.clone(),
            outcome: input.outcome.clone(),
            justification: input.justification.clone(),
            human: input.human,
            by: input.by.clone(),
            ..Default::default()
        };
        match input.outcome.as_str() {
            OUTCOME_DECIDED => {
                if !input.option.is_empty() {
                    input.option = option_id(&c, &input.option);
                }
                if !cur.options.is_empty() && !cur.options.contains(&input.option) {
                    return Err(invalidf(format!(
                        "decision point {} chooses among {:?}, not {:?}",
                        cur.id, cur.options, input.option
                    )));
                }
                if input.confidence < 0.0 || input.confidence > 1.0 {
                    return Err(invalidf(format!(
                        "a confidence is between 0 and 1, not {}",
                        input.confidence
                    )));
                }
                if input.justification.is_empty() {
                    return Err(invalidf("a decision needs a justification"));
                }
                e.option = input.option.clone();
                e.confidence = input.confidence;
            }
            OUTCOME_UNDECIDABLE => {
                e.questions = input
                    .questions
                    .iter()
                    .map(|q| q.trim().to_string())
                    .filter(|q| !q.is_empty())
                    .collect();
                if e.questions.is_empty() || input.justification.is_empty() {
                    return Err(invalidf(
                        "an undecidable ruling says why and asks at least one question",
                    ));
                }
            }
            other => {
                return Err(invalidf(format!(
                    "a ruling is {OUTCOME_DECIDED} or {OUTCOME_UNDECIDABLE}, not {other:?}"
                )));
            }
        }
        self.decision_event(ctx, &mut tx, id, e).await?;
        let d = self
            .settle_decision(ctx, &mut tx, id, &cur.id, &input.by)
            .await?;
        tx.commit(ctx).await?;
        Ok(d)
    }

    /// AnswerQuestion answers an open question of a decision point; process is the one that investigated it, if any.
    pub async fn answer_question(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        question: &str,
        answer: &str,
        process: &str,
        by: &str,
    ) -> Result<DecisionPoint> {
        let answer = answer.trim();
        if answer.is_empty() {
            return Err(invalidf("an answer needs a text"));
        }
        let mut tx = self.change_tx(ctx, id).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        let mut point = String::new();
        for p in c.decision_points_at(self.now(), self.decision_policy.as_deref()) {
            for q in &p.questions {
                if q.id != question {
                    continue;
                }
                if q.status != QUESTION_OPEN {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!("question {question} is {}", q.status),
                    ));
                }
                point = p.id.clone();
            }
        }
        if point.is_empty() {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("question {question} of change {id}"),
            ));
        }
        self.decision_event(
            ctx,
            &mut tx,
            id,
            DecisionEvent {
                op: DECISION_ANSWER_OP.into(),
                point: point.clone(),
                question_id: question.to_string(),
                answer: answer.to_string(),
                process: process.to_string(),
                by: by.to_string(),
                ..Default::default()
            },
        )
        .await?;
        let d = self.decision_of(ctx, &mut tx, id, &point).await?;
        tx.commit(ctx).await?;
        Ok(d)
    }

    /// RatifyDecision accepts or refuses the ruling the policy did not settle (a person's act). Accepted, it decides
    /// the point; refused, the point is open again (the policy may count it as a round).
    pub async fn ratify_decision(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        point: &str,
        accept: bool,
        by: &str,
        comment: &str,
    ) -> Result<DecisionPoint> {
        let mut tx = self.change_tx(ctx, id).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        let cur = pending_point(&c, point, self.now(), self.decision_policy.as_deref())?;
        if cur.status != POINT_RATIFYING {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "decision point {point} is {}: nothing to ratify",
                    cur.status
                ),
            ));
        }
        if !accept && comment.trim().is_empty() {
            return Err(invalidf("refusing a ruling needs a comment"));
        }
        self.decision_event(
            ctx,
            &mut tx,
            id,
            DecisionEvent {
                op: DECISION_RATIFY_OP.into(),
                point: point.to_string(),
                accept,
                comment: comment.to_string(),
                human: true,
                by: by.to_string(),
                ..Default::default()
            },
        )
        .await?;
        let d = self.settle_decision(ctx, &mut tx, id, point, by).await?;
        tx.commit(ctx).await?;
        Ok(d)
    }

    /// DecisionPoints lists the decision points of a change, oldest first.
    pub async fn decision_points(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<DecisionPoint>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c.decision_points_at(self.now(), self.decision_policy.as_deref()))
    }

    /// settleDecision finalizes a point the last event decided (ADR 0009 §5): its option is selected, what the option
    /// wrote joins the change branch and the other open options are rejected.
    pub(crate) async fn settle_decision(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        point: &str,
        by: &str,
    ) -> Result<DecisionPoint> {
        let d = self.decision_of(ctx, tx, id, point).await?;
        if d.status != POINT_DECIDED || d.option.is_empty() {
            return Ok(d);
        }
        let c = tx.change(ctx, id).await?;
        match c.flow(&d.option) {
            Some(f) if f.option.is_some() && f.status == FLOW_OPEN => {}
            _ => return Ok(d), // not an option of the change, or already decided
        }
        self.select_option_tx(ctx, tx, &c, &d.option, by).await?;
        Ok(d)
    }

    pub(crate) async fn decision_of(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        point: &str,
    ) -> Result<DecisionPoint> {
        let c = tx.change(ctx, id).await?;
        c.decision_point(point, self.now(), self.decision_policy.as_deref())
            .ok_or_else(|| {
                Error::wrap(
                    ErrorKind::NotFound,
                    format!("decision point {point} of change {id}"),
                )
            })
    }
}

/// optionID resolves an option of c by id or by name (the names of the open options first).
pub(crate) fn option_id(c: &Change, s: &str) -> String {
    let options = c.options();
    if options.iter().any(|o| o.id == s) {
        return s.to_string();
    }
    for open in [true, false] {
        for o in &options {
            if (o.status == FLOW_OPEN) == open
                && o.option
                    .as_ref()
                    .is_some_and(|spec| spec.name.to_lowercase() == s.to_lowercase())
            {
                return o.id.clone();
            }
        }
    }
    s.to_string()
}

pub(crate) fn pending_point(
    c: &Change,
    point: &str,
    now: DateTime<Utc>,
    policy: Option<&dyn DecisionPolicy>,
) -> Result<DecisionPoint> {
    if point.is_empty() {
        // the only pending point
        let mut pending: Vec<DecisionPoint> = c
            .decision_points_at(now, policy)
            .into_iter()
            .filter(|d| d.pending())
            .collect();
        if pending.len() != 1 {
            return Err(invalidf(format!(
                "change {} has {} pending decision points: name one",
                c.id,
                pending.len()
            )));
        }
        return Ok(pending.remove(0));
    }
    let Some(d) = c.decision_point(point, now, policy) else {
        return Err(Error::wrap(
            ErrorKind::NotFound,
            format!("decision point {point} of change {}", c.id),
        ));
    };
    if !d.pending() {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("decision point {point} is decided"),
        ));
    }
    Ok(d)
}

/// pendingDecisions are the decision points of c not decided yet.
pub(crate) fn pending_decisions(
    c: &Change,
    now: DateTime<Utc>,
    policy: Option<&dyn DecisionPolicy>,
) -> Vec<DecisionPoint> {
    c.decision_points_at(now, policy)
        .into_iter()
        .filter(|d| d.pending())
        .collect()
}
