//! Port of `companion.go`: the choreography of a change (ADR 0036 §3, ADR 0106, ADR 0110).

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_DRAFT, Change, ChangeID, ChangeItem,
};
use goap_graphapi::ChangesFilter;
use goap_methodology::{
    Action, Compiled, FROM_DEPENDENCY, FROM_INTEGRATED, FROM_INTEGRATOR, FROM_SELF, Subscription,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::engine::{Engine, ProcessEvent, StartRequest, obj};
use crate::error::Result;
use crate::process::{
    Process, STATUS_CANCELLED, STATUS_COMPLETED, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK, Step,
};
use crate::triggers::TriggerEvent;

/// COMPANION_PREFIX starts the Trigger of a companion run: "companion:<methodology>/<process>" (ADR 0036 §3).
pub const COMPANION_PREFIX: &str = "companion:";

/// COMPANION_LOCK prefixes the lock serializing the companion runs of a change, so two events of one change never
/// start two, whichever replica handles them (ADR 0124).
const COMPANION_LOCK: &str = "companions:";

pub(crate) const SUPERSEDED_BY: &str = "a newer event of the change";
pub(crate) const SUPERSEDE_LOCK: &str = "supersede:";

impl Process {
    /// is_companion reports whether a run is the companion of a change (a process of a transverse methodology).
    pub fn is_companion(&self) -> bool {
        self.trigger.starts_with(COMPANION_PREFIX)
    }
}

/// StepEvent is a step of a process that completed: what the transverse methodologies react to (ADR 0036 §3).
/// Its JSON keeps Go's field names (no tags there).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct StepEvent {
    /// Path of the step ("software_delivery/design"), its Process and Name, the Action that ran and the Method
    /// chosen.
    #[serde(rename = "Path")]
    pub path: String,
    #[serde(rename = "Process")]
    pub process: String,
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Action")]
    pub action: String,
    #[serde(rename = "Method")]
    pub method: String,
}

fn new_event(e: &Engine, act: &Map<String, Value>) -> Map<String, Value> {
    let mut event = act.clone();
    event.insert("id".into(), Value::String(uuid::Uuid::new_v4().to_string()));
    event.insert("at".into(), json!(e.clock().timestamp_millis()));
    event
}

/// matches reports whether one of the subscriptions takes the event coming from the given side, and whether one
/// that takes it supersedes the work in flight (Subscription.supersede).
pub(crate) fn match_subscriptions(
    subs: &[Subscription],
    ev: &TriggerEvent,
    act: &Map<String, Value>,
    from: &str,
) -> (bool, bool) {
    let (mut ok, mut supersede) = (false, false);
    for sub in subs {
        if sub.event != ev.r#type || sub.origin() != from {
            continue;
        }
        if let Ok(f) = goap_condition::compile_event_filter(&sub.filter)
            && f.matches(act)
        {
            ok = true;
            supersede = supersede || sub.supersede;
        }
    }
    (ok, supersede)
}

/// own_item reports whether an item was produced by an action of the methodology m.
fn own_item(m: &Compiled, it: &ChangeItem) -> bool {
    m.action(&it.produced_by).is_some()
}

/// event_user is the end user of an event as a companion run gets it (TriggerEvent.activation, ADR 0114).
fn event_user(event: &Map<String, Value>) -> String {
    event
        .get("endUser")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

impl Engine {
    /// accompany is the choreography of a change (ADR 0036 §3): no process orchestrates another, they react to the
    /// events of the change they share. The processes of the transverse methodologies run alongside the changes of
    /// the methodologies they apply to (appliesTo): each event they subscribe to (on) runs their companion run on the
    /// change again, with the event in vars.event (the events that arrive while it is at work wait in its inbox);
    /// and when the change moves because of someone else, the processes waiting on it for conditions they do not
    /// establish, and those stuck on it, are tried again.
    pub async fn accompany(self: &Arc<Self>, ctx: &Ctx, ev: &TriggerEvent) {
        let (change_id, mut of, from, companion, mut of_ref);
        if let Some(p) = &ev.process {
            if p.change_id.is_empty() {
                return;
            }
            change_id = p.change_id.clone();
            of = p.methodology.clone();
            from = p.id.clone();
            companion = p.is_companion();
            of_ref = goap_domain::methodology_ref(&of, &p.methodology_version);
        } else if let Some(c) = &ev.change {
            change_id = c.id.clone();
            of = c.methodology.clone();
            from = String::new();
            companion = false;
            of_ref = c.methodology_ref();
        } else {
            return;
        }
        // what someone else did may be what a process waits for
        if matches!(
            ev.r#type.as_str(),
            goap_events::STEP_COMPLETED
                | goap_events::PROCESS_COMPLETED
                | goap_events::CHANGE_ITEM_ADDED
        ) {
            self.retry_blocked(ctx, &change_id, &from).await;
        }
        if let Some(p) = &ev.process
            && p.is_instance()
            && !companion
            && ev.r#type == goap_events::PROCESS_COMPLETED
        {
            // a persistent instance takes the next execution of its inbox, or hands it to the next instance (ADR
            // 0112)
            if let Err(err) = self.next_in_inbox(ctx, &p.id).await {
                tracing::warn!(process = %p.id, err = %err, "instance inbox");
            }
        }
        if companion {
            // a companion's own events do not wake companions; when it completes, it takes the next event of its
            // inbox
            if ev.r#type == goap_events::PROCESS_COMPLETED
                && let Some(p) = &ev.process
                && let Err(err) = self.next_in_inbox(ctx, &p.id).await
            {
                tracing::warn!(process = %p.id, err = %err, "companion inbox");
            }
            return;
        }
        if of.is_empty()
            && let Ok(bb) = self.graph.blackboard(ctx, &change_id).await
        {
            of = bb.change.methodology.clone();
            of_ref = bb.change.methodology_ref();
        }
        let ms = match self.methodologies.list(ctx).await {
            Ok(ms) => ms,
            Err(err) => {
                tracing::warn!(err = %err, "companions: methodologies");
                return;
            }
        };
        let act = ev.activation();
        self.relay(ctx, ev, &act, &change_id).await;
        if !of.is_empty()
            && let Ok(own) = self.methodologies.methodology(ctx, &of_ref).await
        {
            self.react(ctx, &own, ev, &act, &change_id, FROM_SELF, None)
                .await;
        }
        for m in &ms {
            let (ok, supersede) = match_subscriptions(&m.subscriptions(), ev, &act, FROM_SELF);
            if !m.applies_to().contains(&of) || !ok {
                continue;
            }
            if ev.r#type == goap_events::CHANGE_ITEM_ADDED
                && !ev.items.iter().any(|it| !own_item(m, it))
            {
                continue; // its own productions do not wake it
            }
            let event = new_event(self, &act);
            for proc in m.processes() {
                if let Err(err) = self
                    .accompany_process(ctx, m, &proc.name, &change_id, &event, supersede)
                    .await
                {
                    tracing::warn!(methodology = %m.name(), process = %proc.name, change = %change_id, err = %err,
                        "companion");
                }
            }
        }
    }

    /// relay carries the event of a change across the integration relation (ADR 0106): to the change integrating it
    /// (its parent, on.from integrated), to the changes it integrates (its open sub-changes, on.from integrator),
    /// and to the open changes depending on it (ADR 0108, on.from dependency). Each runs the processes of that
    /// methodology on its own change, as a companion run, with the event in vars.event (its "relation" says which
    /// side it came from); the runs of those changes waiting for conditions are tried again.
    async fn relay(
        self: &Arc<Self>,
        ctx: &Ctx,
        ev: &TriggerEvent,
        act: &Map<String, Value>,
        change_id: &ChangeID,
    ) {
        let mut parent = ChangeID::default();
        match &ev.change {
            Some(c) if c.id == *change_id => parent = c.parent_id.clone(),
            _ => {
                if let Ok(bb) = self.graph.blackboard(ctx, change_id).await {
                    parent = bb.change.parent_id.clone();
                }
            }
        }
        let mut targets: Vec<(Change, &str)> = Vec::new();
        if !parent.is_empty()
            && let Ok(p) = self.graph.change(ctx, &parent).await
        {
            targets.push((p, FROM_INTEGRATED));
        }
        let filter = ChangesFilter {
            status: vec![CHANGE_DRAFT, CHANGE_ACTIVE],
            ..Default::default()
        };
        if let Ok(subs) = self.graph.list_changes(ctx, filter).await {
            for s in subs {
                if s.parent_id == *change_id {
                    targets.push((s.clone(), FROM_INTEGRATOR));
                }
                if s.depends_on.contains(change_id) {
                    // a change depending on it (ADR 0108)
                    targets.push((s, FROM_DEPENDENCY));
                }
            }
        }
        for (t, from) in targets {
            self.retry_blocked(ctx, &t.id, "").await;
            if t.methodology.is_empty() {
                continue;
            }
            let Ok(m) = self
                .methodologies
                .methodology(ctx, &t.methodology_ref())
                .await
            else {
                continue;
            };
            let extra = obj(json!({"relation": from}));
            self.react(ctx, &m, ev, act, &t.id, from, Some(&extra))
                .await;
            let (ok, supersede) = match_subscriptions(&m.subscriptions(), ev, act, from);
            if !ok {
                continue;
            }
            let mut event = new_event(self, act);
            event.insert("relation".into(), Value::String(from.to_string()));
            for proc in m.processes() {
                if let Err(err) = self
                    .accompany_process(ctx, &m, &proc.name, &t.id, &event, supersede)
                    .await
                {
                    tracing::warn!(methodology = %m.name(), process = %proc.name, change = %t.id, err = %err, "relay");
                }
            }
        }
    }

    /// react runs the reactive activities of a methodology (ADR 0110) that subscribe to the event coming from the
    /// given side of the change: each as its own companion run on that change, kept and run again in place per
    /// event.
    #[allow(clippy::too_many_arguments)]
    async fn react(
        self: &Arc<Self>,
        ctx: &Ctx,
        m: &Arc<Compiled>,
        ev: &TriggerEvent,
        act: &Map<String, Value>,
        change_id: &ChangeID,
        from: &str,
        extra: Option<&Map<String, Value>>,
    ) {
        for r in m.reactions() {
            let (ok, supersede) = match_subscriptions(&r.on, ev, act, from);
            if !ok {
                continue;
            }
            let raised = ev.r#type == goap_events::CHANGE_ITEM_ADDED
                || (!ev.items.is_empty() && !goap_methodology::standard_event(&ev.r#type));
            if raised && !ev.items.iter().any(|it| !r.owns(&it.produced_by)) {
                continue; // its own productions (and the events they raise) do not wake it
            }
            let mut event = act.clone();
            if let Some(extra) = extra {
                for (k, v) in extra {
                    event.insert(k.clone(), v.clone());
                }
            }
            event.insert("id".into(), Value::String(uuid::Uuid::new_v4().to_string()));
            event.insert("at".into(), json!(self.clock().timestamp_millis()));
            let key = format!("{COMPANION_PREFIX}{}/{}", m.name(), r.activity);
            let intent = format!("{} {} on {}", r.kind, r.activity, ev.r#type);
            if let Err(err) = self
                .companion_run(
                    ctx, m, &key, &r.agent, &r.goal, &intent, change_id, event, supersede,
                )
                .await
            {
                tracing::warn!(methodology = %m.name(), activity = %r.activity, change = %change_id, err = %err,
                    "reaction");
            }
        }
    }

    /// accompany_process runs the companion of a process on a change for an event: it starts it, runs it again when
    /// it has ended, or puts the event in its inbox when it is at work.
    async fn accompany_process(
        self: &Arc<Self>,
        ctx: &Ctx,
        m: &Arc<Compiled>,
        process: &str,
        change_id: &ChangeID,
        event: &Map<String, Value>,
        supersede: bool,
    ) -> Result<()> {
        let key = format!("{COMPANION_PREFIX}{}/{process}", m.name());
        let intent = format!("{process} alongside the change");
        self.companion_run(
            ctx,
            m,
            &key,
            process,
            process,
            &intent,
            change_id,
            event.clone(),
            supersede,
        )
        .await
    }

    /// companion_run runs the companion of key on a change for an event, towards goal with agent: it starts it,
    /// runs it again when it has ended, or puts the event in its inbox when it is at work (ADR 0036 §3, ADR 0110);
    /// an event of a subscription that supersedes interrupts the work and runs it again (restart_for).
    #[allow(clippy::too_many_arguments)]
    async fn companion_run(
        self: &Arc<Self>,
        ctx: &Ctx,
        m: &Arc<Compiled>,
        key: &str,
        agent: &str,
        goal: &str,
        intent: &str,
        change_id: &ChangeID,
        event: Map<String, Value>,
        supersede: bool,
    ) -> Result<()> {
        let _u = self.lock(&format!("{COMPANION_LOCK}{change_id}")).await;
        let all = self.store.list(ctx).await?;
        for p in &all {
            if p.change_id != *change_id || p.trigger != key {
                continue;
            }
            if p.status == STATUS_CANCELLED {
                return Ok(()); // a person stopped it: the events of the change do not bring it back
            }
            if p.status == STATUS_COMPLETED
                || p.status == STATUS_STUCK
                || p.status == STATUS_FAILED
                || p.waits_for_conditions()
            {
                return self.rerun(ctx, &p.id, Some(event)).await;
            }
            if supersede {
                // waiting for the run to let go must not hold the companions, nor the publisher of the event
                let e = self.clone();
                let id = p.id.clone();
                let bctx = ctx.without_cancel();
                self.background(async move {
                    if let Err(err) = e.restart_for(&bctx, &id, event).await {
                        tracing::warn!(process = %id, err = %err, "supersede");
                    }
                });
                return Ok(());
            }
            return self.enqueue(ctx, &p.id, event).await; // at work, or waiting for someone: after this run
        }
        let bb = self.graph.blackboard(ctx, change_id).await?;
        if bb.change.status == CHANGE_APPLIED || bb.change.status == CHANGE_ABANDONED {
            return Ok(());
        }
        // a consequence of what the end user did (ADR 0114)
        let mut who = goap_authz::system(key, &[]).for_user(&event_user(&event));
        who.org = "system".into();
        let mut vars = Map::new();
        vars.insert("event".into(), Value::Object(event));
        let req = StartRequest {
            methodology: m.name().to_string(),
            agent: agent.to_string(),
            goal: goal.to_string(),
            change_id: change_id.clone(),
            owner_org: bb.change.owner_org.clone(),
            trigger: key.to_string(),
            title: bb.change.title.clone(),
            vars,
            intent: format!("{intent}: {}", bb.change.intent),
            ..Default::default()
        };
        let sctx = goap_authz::with_principal(ctx, who);
        let p = self.start(&sctx, req).await?;
        if p.status == STATUS_RUNNING {
            self.schedule_run(&p.id);
        }
        Ok(())
    }

    /// enqueue keeps an event for a companion at work.
    async fn enqueue(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        event: Map<String, Value>,
    ) -> Result<()> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        p.inbox.push(event);
        self.store.put(ctx, &p).await
    }

    /// next_in_inbox runs a completed companion again for the next event of its inbox.
    pub(crate) async fn next_in_inbox(self: &Arc<Self>, ctx: &Ctx, id: &str) -> Result<()> {
        let p = self.store.get(ctx, id).await?;
        if p.inbox.is_empty() {
            return Ok(());
        }
        let _u = self.lock(&format!("{COMPANION_LOCK}{}", p.change_id)).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.inbox.is_empty() {
            return Ok(());
        }
        if p.closed {
            return self.hand_over(ctx, &mut p).await;
        }
        self.rerun(ctx, id, None).await
    }

    /// restart_for interrupts a companion at work and runs it again for the event, on the change as it is now (ADR
    /// 0110): its sub-processes are cancelled, its run in flight is interrupted and let go without failing, then it
    /// starts again with what it waited for, what it tried and its inbox dropped.
    pub(crate) async fn restart_for(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        event: Map<String, Value>,
    ) -> Result<()> {
        // one at a time per run: the last one runs on the latest change
        let _s = self.lock(&format!("{SUPERSEDE_LOCK}{id}")).await;
        self.cancelling
            .lock()
            .insert(id.to_string(), SUPERSEDED_BY.to_string());
        struct Clear<'a>(&'a Engine, String, bool);
        impl Drop for Clear<'_> {
            fn drop(&mut self) {
                if self.2 {
                    self.0.cancelling.lock().remove(&self.1);
                }
            }
        }
        let mut clear = Clear(self, id.to_string(), true);
        let all = self.store.list(ctx).await?;
        for c in &all {
            if c.parent_id == id && !c.status.terminal() {
                self.cancel_as(
                    ctx,
                    &c.id,
                    SUPERSEDED_BY,
                    "superseded by a newer event of the change",
                )
                .await
                .map_err(|err| err.context(format!("sub-process {}", c.id)))?;
            }
        }
        self.interrupt(ctx, id, SUPERSEDED_BY).await;
        let unlock = self.lock(id).await; // the run lets go
        let mut p = self.store.get(ctx, id).await?;
        if p.status.terminal() && p.status != STATUS_COMPLETED && p.status != STATUS_FAILED {
            return Ok(()); // cancelled meanwhile: a person stopped it
        }
        let typ = event.get("type").cloned().unwrap_or(Value::Null);
        let user = event_user(&event);
        p.vars.insert("event".into(), Value::Object(event));
        p.inbox = Vec::new();
        p.children = Default::default();
        // the run again is a consequence of the newer event's end user
        p.initiator = p.initiator.clone().for_user(&user);
        p.status = STATUS_RUNNING.into();
        p.error = String::new();
        p.plan = Vec::new();
        p.pending = None;
        p.disabled = Default::default();
        p.runs += 1;
        let cause = obj(json!({"change": p.change_id.as_str(), "event": typ}));
        self.queue(ctx, &mut p, "superseded", Some(cause));
        let res = self.save(ctx, &mut p, "step").await;
        self.cancelling.lock().remove(id); // before the new run can look
        clear.2 = false;
        drop(unlock);
        if res.is_ok() {
            self.schedule_run(id);
        }
        res
    }

    /// supersede_runs cancels the runs of a trigger still at work on a change, before the trigger starts a new one
    /// for a newer event (methodology.Trigger.supersede): what they do is about a state of the change that is gone.
    pub(crate) async fn supersede_runs(
        self: &Arc<Self>,
        ctx: &Ctx,
        change_id: &ChangeID,
        trigger: &str,
        by: &str,
    ) {
        let Ok(all) = self.store.list(ctx).await else {
            return;
        };
        for p in &all {
            if p.change_id == *change_id
                && p.trigger == trigger
                && p.parent_id.is_empty()
                && !p.status.terminal()
                && let Err(err) = self
                    .cancel_as(ctx, &p.id, by, "superseded by a newer event of the change")
                    .await
            {
                tracing::warn!(process = %p.id, err = %err, "supersede");
            }
        }
    }

    /// retry_blocked tries again the processes of a change that wait for conditions established outside them, or
    /// are stuck (companions aside, their events run them again, and the one the event comes from).
    async fn retry_blocked(self: &Arc<Self>, ctx: &Ctx, change_id: &ChangeID, except: &str) {
        let Ok(all) = self.store.list(ctx).await else {
            return;
        };
        for p in &all {
            if p.change_id == *change_id
                && (p.status == STATUS_STUCK || p.waits_for_conditions())
                && p.id != except
                && !p.is_companion()
                && let Err(err) = self.rerun(ctx, &p.id, None).await
            {
                tracing::warn!(process = %p.id, err = %err, "retry");
            }
        }
    }

    /// rerun runs an ended process, or one waiting for conditions, again on the change as it is now: for a
    /// companion, for the event given, else the next of its inbox.
    pub(crate) async fn rerun(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        event: Option<Map<String, Value>>,
    ) -> Result<()> {
        let unlock = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_COMPLETED
            && p.status != STATUS_STUCK
            && p.status != STATUS_FAILED
            && !p.waits_for_conditions()
        {
            return Ok(());
        }
        let mut event = event;
        if event.is_none() && !p.inbox.is_empty() {
            event = Some(p.inbox.remove(0));
        }
        let mut typ = Value::Null;
        if let Some(event) = event {
            typ = event.get("type").cloned().unwrap_or(Value::Null);
            let user = event_user(&event);
            p.vars.insert("event".into(), Value::Object(event));
            // this run is a consequence of the event's end user
            p.initiator = p.initiator.clone().for_user(&user);
        }
        p.status = STATUS_RUNNING.into();
        p.error = String::new();
        p.plan = Vec::new();
        p.pending = None;
        p.disabled = Default::default(); // what failed before may work on the change as it is now
        p.runs += 1;
        let cause = obj(json!({"change": p.change_id.as_str(), "event": typ}));
        self.queue(ctx, &mut p, "change moved", Some(cause));
        let res = self.save(ctx, &mut p, "step").await;
        drop(unlock);
        if res.is_ok() {
            self.schedule_run(id);
        }
        res
    }

    /// step_completed publishes that a step of p's process completed (step.completed).
    pub(crate) async fn step_completed(
        &self,
        ctx: &Ctx,
        p: &Process,
        m: &Compiled,
        a: &Action,
        st: &Step,
    ) {
        let Some(events) = &self.events else {
            return;
        };
        let mut step = StepEvent {
            path: a.step.clone(),
            action: a.name.clone(),
            method: st.specialization.clone(),
            ..Default::default()
        };
        if let Some(s) = m.step_by_path(&a.step) {
            step.process = s.owner.clone();
            step.name = s.step.name.clone();
        }
        let ev = ProcessEvent {
            event: goap_events::BROKER_STEP_COMPLETED.into(),
            process: Some(p.clone()),
            step: Some(step),
            log: None,
            time: self.clock(),
        };
        let subject = format!(
            "goap.process.{}.{}",
            p.id,
            goap_events::BROKER_STEP_COMPLETED
        );
        if let Err(err) = events
            .publish(ctx, &subject, &crate::ports::Payload::new(&ev))
            .await
        {
            tracing::warn!(err = %err, "publish failed");
        }
    }
}
