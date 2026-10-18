//! Persistent agents (ADR 0112, Go's `persistent.go`): an agent declaring `persistent` has one instance per scope, a
//! process the engine keeps. Every new execution of that agent on that scope goes to the instance: it runs again in
//! place when it has ended, or the event waits in its inbox while it is at work. Its llm actions get back the
//! exchanges they had with the model. An instance is closed when a run completes with its `until` conditions holding
//! (or when a person cancels it).

use std::sync::Arc;

use chrono::{DateTime, SecondsFormat, Utc};
use goap_ctx::Ctx;
use goap_intent::Turn as IntentTurn;
use goap_llm::Message;
use goap_methodology::{Compiled, Persistence};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::engine::{Engine, StartRequest, obj};
use crate::error::Result;
use crate::process::{
    Process, STATUS_CANCELLED, STATUS_COMPLETED, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK,
};

/// INSTANCE_LOCK prefixes the lock serializing the lookup and the creation of the instance of a key (ADR 0124).
pub const INSTANCE_LOCK: &str = "instance:";

/// Turn is one exchange of an llm action of a persistent instance with the model.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Turn {
    #[serde(with = "goap_domain::gotime")]
    pub at: DateTime<Utc>,
    pub run: i64,
    pub action: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub event: String,
    pub prompt: String,
    pub answer: String,
}

impl Default for Turn {
    fn default() -> Self {
        Turn {
            at: goap_domain::gotime::zero(),
            run: 0,
            action: String::new(),
            event: String::new(),
            prompt: String::new(),
            answer: String::new(),
        }
    }
}

const MAX_TURN_PROMPT: usize = 2000;
const MAX_TURN_ANSWER: usize = 4000;

impl Process {
    /// is_instance reports whether the process is the instance of a persistent agent.
    pub fn is_instance(&self) -> bool {
        !self.instance.is_empty()
    }
}

fn event_of_vars(vars: &Map<String, Value>) -> Option<&Map<String, Value>> {
    vars.get("event").and_then(Value::as_object)
}

impl Engine {
    /// instance_key is the key of the instance a start request goes to ("" when the agent is not persistent, or the
    /// scope names nothing for this request).
    pub(crate) async fn instance_key(
        &self,
        ctx: &Ctx,
        req: &StartRequest,
    ) -> Result<(String, Option<Persistence>)> {
        if req.agent.is_empty() || req.methodology.is_empty() {
            return Ok((String::new(), None));
        }
        let m = self
            .methodologies
            .methodology(ctx, &req.methodology)
            .await?;
        let Some(ag) = m.agent(&req.agent) else {
            return Ok((String::new(), None));
        };
        let Some(pers) = ag.persistent.clone() else {
            return Ok((String::new(), None));
        };
        let empty = Map::new();
        let event = event_of_vars(&req.vars).unwrap_or(&empty);
        let scope = Persistence::resolve(Some(&pers), req.change_id.as_ref(), event);
        if scope.is_empty() {
            return Ok((String::new(), None));
        }
        Ok((format!("{}/{}@{scope}", m.name(), ag.name), Some(pers)))
    }

    /// open_instance is the open instance of key, the latest one.
    async fn open_instance(&self, ctx: &Ctx, key: &str) -> Result<Option<Process>> {
        let all = self.store.list(ctx).await?;
        let mut out: Option<Process> = None;
        for p in all {
            if p.instance != key || p.closed || p.status == STATUS_CANCELLED {
                continue;
            }
            if out.as_ref().is_none_or(|o| p.created_at > o.created_at) {
                out = Some(p);
            }
        }
        Ok(out)
    }

    /// resume_instance hands a start request to the open instance of key: (process, handled, fresh).
    pub(crate) async fn resume_instance(
        self: &Arc<Self>,
        ctx: &Ctx,
        key: &str,
        req: &StartRequest,
    ) -> Result<(Option<Process>, bool, bool)> {
        let Some(open) = self.open_instance(ctx, key).await? else {
            return Ok((None, false, false));
        };
        let _u = self.lock(&open.id).await;
        let mut p = self.store.get(ctx, &open.id).await?;
        if !p.change_id.is_empty()
            && let Ok(ch) = self.graph.change(ctx, &p.change_id).await
            && (ch.status == goap_domain::CHANGE_APPLIED
                || ch.status == goap_domain::CHANGE_ABANDONED)
        {
            p.closed = true; // its change is over: so is the instance
            self.save(ctx, &mut p, "step").await?;
            return Ok((None, false, false));
        }
        let ended = p.status == STATUS_COMPLETED
            || p.status == STATUS_STUCK
            || p.status == STATUS_FAILED
            || p.waits_for_conditions();
        if !ended {
            if !req.parent_id.is_empty() && req.parent_id != p.parent_id {
                return Ok((None, false, true));
            }
            if let Some(event) = event_of_vars(&req.vars) {
                p.inbox.push(event.clone());
                self.save(ctx, &mut p, "step").await?;
                return Ok((Some(p), true, false));
            }
            return Ok((Some(p), true, false)); // at work on it already
        }
        p.vars.extend(req.vars.clone());
        if !req.goal.is_empty() {
            p.goal = req.goal.clone();
        }
        if !req.parent_id.is_empty() {
            p.parent_id = req.parent_id.clone();
            p.step = req.step.clone();
        }
        if !req.cause.is_empty() {
            p.cause = req.cause.clone();
        }
        // the execution acts for whoever asks for it now
        let who = goap_authz::principal_from(ctx);
        if !who.subject.is_empty() {
            p.initiator = who;
        }
        if !req.intent.is_empty() {
            p.intent
                .turns
                .push(IntentTurn::new("user", req.intent.clone()));
            self.log_turns(ctx, &p.id, &[IntentTurn::new("user", req.intent.clone())])
                .await?;
        }
        p.status = STATUS_RUNNING.into();
        p.error = String::new();
        p.plan = Vec::new();
        p.pending = None;
        p.disabled.clear();
        p.runs += 1;
        self.check_roles(ctx, &p).await?;
        let cause = obj(
            json!({"instance": key, "run": p.runs, "parent": req.parent_id, "trigger": req.trigger}),
        );
        self.queue(ctx, &mut p, "new execution", Some(cause));
        self.save(ctx, &mut p, "step").await?;
        Ok((Some(p), true, false))
    }

    /// hand_over starts a new instance for the events left in the inbox of an instance that closed.
    pub(crate) async fn hand_over(self: &Arc<Self>, ctx: &Ctx, p: &mut Process) -> Result<()> {
        if p.inbox.is_empty() {
            return Ok(());
        }
        let m = self.methodology_of(ctx, p).await?;
        let pers = m.agent(&p.agent).and_then(|a| a.persistent.clone());
        let event = p.inbox[0].clone();
        let rest: Vec<Map<String, Value>> = p.inbox[1..].to_vec();
        let mut vars = p.vars.clone();
        vars.insert("event".into(), Value::Object(event));
        let mut req = StartRequest {
            methodology: p.methodology.clone(),
            agent: p.agent.clone(),
            goal: p.goal.clone(),
            trigger: p.trigger.clone(),
            title: p.title.clone(),
            owner_org: p.org.clone(),
            project_id: p.project.clone(),
            vars,
            intent: format!("next cycle of {} after {}", p.agent, p.id),
            ..Default::default()
        };
        if Persistence::scope_path(pers.as_ref()) == goap_methodology::SCOPE_CHANGE {
            req.change_id = p.change_id.clone();
        } else {
            req.baseline_id = self
                .latest_baseline(ctx, goap_domain::namespace_of(m.namespace()))
                .await?;
        }
        {
            let _u = self.lock(&p.id).await;
            p.inbox = Vec::new();
            self.save(ctx, p, "step").await?;
        }
        let mut next = self
            .start(&goap_authz::with_principal(ctx, p.initiator.clone()), req)
            .await?;
        if !rest.is_empty() {
            let _u = self.lock(&next.id).await;
            if let Ok(mut cur) = self.store.get(ctx, &next.id).await {
                cur.inbox.extend(rest);
                let _ = self.store.put(ctx, &cur).await;
                next = cur;
            }
        }
        if next.status == STATUS_RUNNING {
            self.schedule_run(&next.id);
        }
        Ok(())
    }

    /// remember adds the exchange of an llm action to the memory of an instance, keeping the last turns.
    pub(crate) fn remember(&self, p: &mut Process, m: &Compiled, action: &str, t: Option<Turn>) {
        let Some(mut t) = t else {
            return;
        };
        if !p.is_instance() {
            return;
        }
        let pers = m.agent(&p.agent).and_then(|a| a.persistent.clone());
        let keep = Persistence::turns(pers.as_ref());
        if keep == 0 {
            return;
        }
        t.at = self.clock();
        t.run = p.runs;
        t.action = action.to_string();
        if let Some(ev) = event_of_vars(&p.vars) {
            t.event = ev
                .get("type")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string();
        }
        t.prompt = cut(&t.prompt, MAX_TURN_PROMPT);
        t.answer = cut(&t.answer, MAX_TURN_ANSWER);
        p.memory.push(t);
        let keep = keep as usize;
        if p.memory.len() > keep {
            p.memory.drain(..p.memory.len() - keep);
        }
    }
}

/// close_instance closes the instance of a persistent agent whose run completed with its until conditions holding.
pub(crate) fn close_instance(p: &mut Process, m: &Compiled) {
    if !p.is_instance() {
        return;
    }
    if let Some(pers) = m.agent(&p.agent).and_then(|a| a.persistent.as_ref())
        && !pers.until.is_empty()
        && p.world.satisfies(&pers.until)
    {
        p.closed = true;
    }
}

/// memory_messages are the exchanges an instance had with the model, given back before the prompt of its next call.
pub(crate) fn memory_messages(p: &Process) -> Option<Vec<Message>> {
    if !p.is_instance() || p.memory.is_empty() {
        return None;
    }
    let mut out = Vec::with_capacity(2 * p.memory.len());
    for t in &p.memory {
        let mut head = format!(
            "[earlier execution {} of this instance, {}, action {}",
            t.run,
            t.at.to_rfc3339_opts(SecondsFormat::Secs, true),
            t.action
        );
        if !t.event.is_empty() {
            head += &format!(", on {}", t.event);
        }
        out.push(Message {
            role: "user".into(),
            content: format!("{head}]\n{}", t.prompt),
            ..Default::default()
        });
        out.push(Message {
            role: "assistant".into(),
            content: t.answer.clone(),
            ..Default::default()
        });
    }
    Some(out)
}

/// MEMORY_NOTE tells the model the exchanges before the prompt are its own earlier work on the same scope.
pub(crate) const MEMORY_NOTE: &str = "\n\nYou are a persistent agent: the exchanges before the last message are your earlier executions on this same scope. Go on from them: keep what still holds, say what you change and why, do not start over.";

/// cut keeps the first max bytes of s, on a rune boundary.
pub(crate) fn cut(s: &str, max: usize) -> String {
    if s.len() <= max {
        return s.to_string();
    }
    let mut n = max;
    while n > 0 && !s.is_char_boundary(n) {
        n -= 1;
    }
    format!("{} (...)", &s[..n])
}
