//! Port of `asks.go` (ADR 0117): what a change waits for from people. Every task a run of the change waits on a
//! person for, and the decision points only a person may settle, gathered in one list with whom each one is
//! addressed to and whether the caller may answer it. A read model: nothing is stored.

use chrono::{DateTime, Utc};
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::{ChangeID, DecisionPoint, POINT_RATIFYING, QUESTION_OPEN};
use serde::{Deserialize, Serialize};

use crate::engine::{Engine, first_non_empty};
use crate::error::{Error, Result};
use crate::process::{
    Process, STATUS_CLARIFYING, STATUS_STUCK, STATUS_WAITING, TASK_AGENT, TASK_APPROVAL,
    TASK_CONDITION, TASK_INPUT, TASK_RELAUNCHED, TASK_UNBLOCK,
};

/// A run waits for the answer to a question about its intent.
pub const ASK_CLARIFICATION: &str = "clarification";
/// A question blocks a decision point of the change.
pub const ASK_QUESTION: &str = "question";
/// A decision point only a person may rule, or a ruling to ratify.
pub const ASK_DECISION: &str = "decision";

/// Ask is one thing the change waits for from a person.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Ask {
    /// ID is stable while the ask stands: "process:<id>:<step>:<kind>", "question:<id>", "decision:<point>".
    pub id: String,
    pub kind: String,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub step: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    /// Roles are the roles the ask is addressed to (any of them answers); Owner the person it is addressed to when
    /// no role is.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub roles: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    /// Since is when the change started waiting.
    #[serde(with = "goap_domain::gotime")]
    pub since: DateTime<Utc>,
    /// MayAnswer tells whether the caller may answer it.
    pub may_answer: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub point: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question: String,
}

impl Engine {
    /// change_asks lists what the change waits for from people, the oldest first, judged for the caller of ctx.
    pub async fn change_asks(&self, ctx: &Ctx, change_id: &ChangeID) -> Result<Vec<Ask>> {
        let who = goap_authz::principal_from(ctx);
        let all = self.store.list(ctx).await?;
        let mut out = Vec::new();
        for p in &all {
            if p.change_id != *change_id {
                continue;
            }
            if let Some(a) = self.process_ask(ctx, p, &who).await? {
                out.push(a);
            }
        }
        let bb = self.blackboard(ctx, change_id, "").await?;
        out.extend(decision_asks(&goap_domain::decision_points_of(&bb)));
        out.sort_by_key(|a| a.since);
        Ok(out)
    }

    /// process_ask is the ask of a run waiting on a person, if it waits on one.
    async fn process_ask(&self, ctx: &Ctx, p: &Process, who: &Principal) -> Result<Option<Ask>> {
        let mut a = Ask {
            process_id: p.id.clone(),
            agent: p.agent.clone(),
            since: p.updated_at,
            ..Default::default()
        };
        if p.status == STATUS_CLARIFYING {
            a.id = format!("process:{}:clarification", p.id);
            a.kind = ASK_CLARIFICATION.into();
            a.title = p.question.clone();
            a.owner = p.initiator.subject.clone();
            a.may_answer = true;
            return Ok((!a.title.is_empty()).then_some(a));
        }
        let Some(t) = &p.pending else {
            return Ok(None);
        };
        if p.status != STATUS_WAITING && p.status != STATUS_STUCK {
            return Ok(None);
        }
        if t.kind == TASK_AGENT || t.kind == TASK_RELAUNCHED {
            // a sub-agent, or the decision on another run's flow (that run carries the ask)
            return Ok(None);
        }
        a.id = format!("process:{}:{}:{}", p.id, t.step, t.kind);
        a.kind = t.kind.clone();
        a.action = t.action.clone();
        a.detail = t.instructions.clone();
        if t.step >= 0
            && let Some(st) = p.steps.get(t.step as usize)
            && !goap_domain::gotime::is_zero(&st.started_at)
        {
            a.since = st.started_at;
        }
        let sc = t.context.as_ref().or(p.step.as_ref());
        if let Some(sc) = sc {
            a.step = sc.path.clone();
        }
        a.title = t.description.clone();
        if a.title.is_empty()
            && let Some(sc) = sc
        {
            a.title = first_non_empty(&[&sc.description, &sc.name]);
        }
        if a.title.is_empty() {
            a.title = t.action.clone();
        }
        let m = self.methodology_of(ctx, p).await?;
        let allowed: Result<()> = match t.kind.as_str() {
            TASK_INPUT => {
                if let Some(act) = m.action(&t.action) {
                    a.roles = self.run_roles(&m, p, act);
                }
                if let Some(r) = t.context.as_ref().and_then(|c| c.roles.as_ref())
                    && !r.responsible.is_empty()
                {
                    append_role(&mut a.roles, &r.responsible);
                }
                self.may_submit(ctx, p, &m, who).await
            }
            TASK_APPROVAL => {
                a.roles = t.roles.clone();
                if let Some(r) = t.context.as_ref().and_then(|c| c.roles.as_ref())
                    && !r.accountable.is_empty()
                {
                    append_role(&mut a.roles, &r.accountable);
                }
                yes(self.may_approve(ctx, p, who).await)
            }
            TASK_CONDITION | TASK_UNBLOCK => {
                if let Some(r) = p.step.as_ref().and_then(|s| s.roles.as_ref())
                    && !r.accountable.is_empty()
                {
                    a.roles = vec![r.accountable.clone()];
                }
                yes(self.may_unblock(ctx, p, who).await)
            }
            _ => Ok(()),
        };
        if a.roles.is_empty() {
            a.owner = p.initiator.end_user().to_string();
        }
        match allowed {
            Ok(()) => a.may_answer = true,
            Err(err) if err.is_forbidden() => {}
            Err(err) => return Err(err),
        }
        Ok(Some(a))
    }
}

/// decision_asks are the open questions of the decision points of a change and the points only a person may
/// settle.
pub(crate) fn decision_asks(points: &[DecisionPoint]) -> Vec<Ask> {
    let mut out = Vec::new();
    for d in points {
        if !d.pending() {
            continue;
        }
        let mut asked = false;
        for q in &d.questions {
            if q.status != QUESTION_OPEN {
                continue;
            }
            asked = true;
            out.push(Ask {
                id: format!("question:{}", q.id),
                kind: ASK_QUESTION.into(),
                title: q.text.clone(),
                detail: d.question.clone(),
                process_id: q.process.clone(),
                since: q.asked_at,
                may_answer: true,
                point: d.id.clone(),
                question: q.id.clone(),
                ..Default::default()
            });
        }
        if asked {
            continue;
        }
        if d.status == POINT_RATIFYING || d.needs_human() {
            let mut a = Ask {
                id: format!("decision:{}", d.id),
                kind: ASK_DECISION.into(),
                title: d.question.clone(),
                detail: d.escalation.clone(),
                since: d.opened_at,
                may_answer: true,
                point: d.id.clone(),
                ..Default::default()
            };
            if let Some(r) = &d.ruling {
                a.since = r.at;
            }
            out.push(a);
        }
    }
    out
}

/// yes turns a permission check into the error of a refusal.
fn yes(r: Result<bool>) -> Result<()> {
    match r {
        Err(err) => Err(err),
        Ok(false) => Err(Error::Authz(goap_authz::Error::Forbidden(String::new()))),
        Ok(true) => Ok(()),
    }
}

fn append_role(roles: &mut Vec<String>, r: &str) {
    if !roles.iter().any(|x| x == r) {
        roles.push(r.to_string());
    }
}
