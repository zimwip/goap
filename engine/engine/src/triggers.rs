//! The trigger manager, running agents on events and schedules.

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, Weak};
use std::time::Duration;

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{BaselineID, Change, ChangeItem, HeaderValue, TYPE_SIGNAL};
use goap_methodology::{TARGET_EVENT_CHANGE, TRIGGER_EVENT, TRIGGER_SCHEDULE, Trigger};
use parking_lot::{Mutex, RwLock};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};
use tokio::task::AbortHandle;

use crate::companion::StepEvent;
use crate::cron::{self, Schedule};
use crate::engine::{Clock, Engine, StartRequest, obj};
use crate::error::{Error, Result};
use crate::process::{Process, STATUS_RUNNING};

/// TriggerEvent is an event that may start agents.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct TriggerEvent {
    #[serde(rename = "type")]
    pub r#type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change: Option<Change>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub process: Option<Process>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    /// Items are the change items written by the operation that raised the event (set for change.item_added /
    /// change.signal), so a trigger filter can select on their content (event.items.exists(...)).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<ChangeItem>,
    /// Step is the step that completed (step.completed, ADR 0036 §3).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub step: Option<StepEvent>,
    /// Fields are the fields of the header a change.edited event changed (ADR 0110).
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub fields: BTreeMap<String, HeaderValue>,
    /// EndUser is the person whose action raised a change event (domain.ChangeEvent.EndUser, ADR 0114).
    #[serde(rename = "endUser", skip_serializing_if = "String::is_empty")]
    pub end_user: String,
}

impl TriggerEvent {
    /// end_user is the person the runs an event sets off act for (ADR 0114): the one who raised a change event,
    /// else the end user of the process the event comes from.
    pub fn end_user_of(&self) -> String {
        if !self.end_user.is_empty() {
            return self.end_user.clone();
        }
        if let Some(p) = &self.process {
            return p.initiator.end_user().to_string();
        }
        String::new()
    }

    /// activation is the event as CEL filters and vars.event see it.
    pub fn activation(&self) -> Map<String, Value> {
        let mut out = obj(
            json!({"type": self.r#type, "change": {}, "process": {}, "methodology": {}, "step": {},
            "fields": {}, "endUser": self.end_user_of()}),
        );
        if !self.fields.is_empty() {
            let mut fields = Map::new();
            for (k, v) in &self.fields {
                fields.insert(k.clone(), json!({"from": v.from, "to": v.to}));
            }
            out.insert("fields".into(), Value::Object(fields));
        }
        if let Some(s) = &self.step {
            out.insert(
                "step".into(),
                json!({"path": s.path, "process": s.process, "name": s.name, "action": s.action, "method": s.method}),
            );
        }
        if let Some(c) = &self.change {
            out.insert(
                "change".into(),
                json!({"id": c.id.as_str(), "title": c.title, "intent": c.intent, "status": c.status.as_str(),
                    "methodology": c.methodology, "goal": c.goal, "baseline": c.baseline_id.as_str(),
                    "items": c.items.len() as i64, "data": c.data, "state": c.state, "parent": c.parent_id.as_str(),
                    "project": c.project_id}),
            );
        }
        if let Some(p) = &self.process {
            out.insert(
                "process".into(),
                json!({"id": p.id, "agent": p.agent, "goal": p.goal, "status": p.status.as_str(),
                    "methodology": p.methodology, "trigger": p.trigger, "change": p.change_id.as_str(),
                    "parent": p.parent_id}),
            );
        }
        if !self.methodology.is_empty() {
            out.insert(
                "methodology".into(),
                json!({"name": self.methodology, "version": self.version}),
            );
        }
        if !self.items.is_empty() {
            let items: Vec<Value> = self
                .items
                .iter()
                .map(|it| {
                    json!({"type": it.r#type.as_str(), "target": it.target, "data": it.data,
                        "producedBy": it.produced_by, "recipient": it.recipient})
                })
                .collect();
            out.insert("items".into(), Value::Array(items));
        }
        out
    }
}

/// TriggerState is the runtime state of a trigger.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct TriggerState {
    pub methodology: String,
    pub agent: String,
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(rename = "type")]
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub event: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub schedule: String,
    pub enabled: bool,
    pub fires: i64,
    #[serde(with = "goap_domain::gotime::option")]
    pub last_fired: Option<DateTime<Utc>>,
    #[serde(with = "goap_domain::gotime::option")]
    pub next_fire: Option<DateTime<Utc>>,
    #[serde(rename = "lastProcessId", skip_serializing_if = "String::is_empty")]
    pub last_process_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub last_error: String,
}

impl TriggerState {
    /// key identifies a trigger: "<methodology>/<agent>/<trigger>".
    pub fn key(&self) -> String {
        format!("{}/{}/{}", self.methodology, self.agent, self.name)
    }
}

struct TriggerEntry {
    state: TriggerState,
    def: Trigger,
    filter: Option<goap_condition::EventFilter>,
    cron_id: u64,
}

struct CronJob {
    schedule: Schedule,
    handle: Option<AbortHandle>,
}

#[derive(Default)]
struct ManagerState {
    entries: HashMap<String, TriggerEntry>,
    /// started: the scheduler runs (Start, until Stop).
    started: bool,
    running: bool,
    next_id: u64,
    jobs: HashMap<u64, CronJob>,
}

/// LeaderFn reports whether this replica may fire schedule (cron) triggers.
pub type LeaderFn = Arc<dyn Fn() -> bool + Send + Sync>;

/// TriggerManager runs agents automatically on events and schedules. Runs use a service identity
/// "system:trigger:<key>" with the roles declared by the trigger; changes they open are marked so that a trigger
/// never fires on its own output (loop protection), and a trigger fires at most once per min_interval.
///
/// With several engine replicas, event delivery is deduplicated by the caller binding every replica's durable
/// consumer to the same name (competing consumers); schedule (cron) firing is gated by leader instead: only the
/// replica for which leader() is true fires a cron entry. reload, states and a manual fire stay available on every
/// replica.
pub struct TriggerManager {
    pub engine: Arc<Engine>,
    /// min_interval between two fires of the same trigger (zero: 2s).
    pub min_interval: Duration,
    /// leader: None means always leader (single-process deployments, tests).
    pub leader: Option<LeaderFn>,
    st: Mutex<ManagerState>,
    now: RwLock<Option<Clock>>,
    me: Mutex<Weak<TriggerManager>>,
}

impl TriggerManager {
    pub fn new(engine: Arc<Engine>) -> Arc<TriggerManager> {
        let t = Arc::new(TriggerManager {
            engine,
            min_interval: Duration::ZERO,
            leader: None,
            st: Mutex::new(ManagerState::default()),
            now: RwLock::new(None),
            me: Mutex::new(Weak::new()),
        });
        *t.me.lock() = Arc::downgrade(&t);
        t
    }

    /// with builds a manager with its settings.
    pub fn with(
        engine: Arc<Engine>,
        min_interval: Duration,
        leader: Option<LeaderFn>,
    ) -> Arc<TriggerManager> {
        let t = Arc::new(TriggerManager {
            engine,
            min_interval,
            leader,
            st: Mutex::new(ManagerState::default()),
            now: RwLock::new(None),
            me: Mutex::new(Weak::new()),
        });
        *t.me.lock() = Arc::downgrade(&t);
        t
    }

    /// set_clock replaces the clock of the manager (tests); None restores the real one.
    pub fn set_clock(&self, c: Option<Clock>) {
        *self.now.write() = c;
    }

    fn is_leader(&self) -> bool {
        self.leader.as_ref().is_none_or(|l| l())
    }

    fn clock(&self) -> DateTime<Utc> {
        match &*self.now.read() {
            Some(c) => c(),
            None => Utc::now(),
        }
    }

    /// start loads the triggers, starts the scheduler and reloads every minute until ctx is done.
    pub async fn start(self: &Arc<Self>, ctx: &Ctx) {
        {
            let mut st = self.st.lock();
            if !st.started {
                st.started = true;
                st.running = true;
            }
        }
        if let Err(err) = self.reload(ctx).await {
            tracing::warn!(err = %err, "triggers");
        }
        let t = self.clone();
        let ctx = ctx.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(Duration::from_secs(60));
            tick.tick().await;
            loop {
                tokio::select! {
                    _ = ctx.done() => {
                        t.stop();
                        return;
                    }
                    _ = tick.tick() => {
                        if let Err(err) = t.reload(&ctx).await {
                            tracing::warn!(err = %err, "triggers reload");
                        }
                    }
                }
            }
        });
    }

    /// stop stops the scheduler.
    pub fn stop(&self) {
        let mut st = self.st.lock();
        st.running = false;
        for job in st.jobs.values_mut() {
            if let Some(h) = job.handle.take() {
                h.abort();
            }
        }
    }

    /// add_job schedules the firing of key (robfig's AddFunc), returning its entry id.
    fn add_job(&self, st: &mut ManagerState, schedule: Schedule, key: String) -> u64 {
        st.next_id += 1;
        let id = st.next_id;
        let handle = if st.running {
            let weak = self.me.lock().clone();
            let sched = schedule.clone();
            let task = tokio::spawn(async move {
                loop {
                    let Some(t) = weak.upgrade() else { return };
                    let now = t.clock();
                    let Some(next) = sched.next(now) else { return };
                    drop(t);
                    let wait = (next - now).to_std().unwrap_or_default();
                    tokio::time::sleep(wait).await;
                    let Some(t) = weak.upgrade() else { return };
                    t.cron_fire(&key).await;
                }
            });
            Some(task.abort_handle())
        } else {
            None
        };
        st.jobs.insert(id, CronJob { schedule, handle });
        id
    }

    /// reload rebuilds the triggers from the published methodologies, keeping the counters of unchanged triggers.
    pub async fn reload(&self, ctx: &Ctx) -> Result<()> {
        let ms = self.engine.methodologies.list(ctx).await?;
        let mut st = self.st.lock();
        let old = std::mem::take(&mut st.entries);
        for e in old.values() {
            if e.cron_id != 0
                && let Some(mut job) = st.jobs.remove(&e.cron_id)
                && let Some(h) = job.handle.take()
            {
                h.abort();
            }
        }
        for m in &ms {
            for ag in m.agent_list() {
                for def in &ag.triggers {
                    let mut e = TriggerEntry {
                        def: def.clone(),
                        state: TriggerState {
                            methodology: m.name().to_string(),
                            agent: ag.name.clone(),
                            name: def.name.clone(),
                            description: def.description.clone(),
                            r#type: def.r#type.clone(),
                            event: def.event.clone(),
                            schedule: def.schedule.clone(),
                            enabled: def.enabled,
                            ..Default::default()
                        },
                        filter: None,
                        cron_id: 0,
                    };
                    if let Some(prev) = old.get(&e.state.key()) {
                        e.state.fires = prev.state.fires;
                        e.state.last_fired = prev.state.last_fired;
                        e.state.last_process_id = prev.state.last_process_id.clone();
                        e.state.last_error = prev.state.last_error.clone();
                    }
                    if def.r#type == TRIGGER_EVENT {
                        match goap_condition::compile_event_filter(&def.filter) {
                            Ok(f) => e.filter = Some(f),
                            Err(err) => {
                                e.state.last_error = err.to_string();
                                e.state.enabled = false;
                            }
                        }
                    }
                    let key = e.state.key();
                    if def.r#type == TRIGGER_SCHEDULE && def.enabled && st.started {
                        match cron::parse(&def.schedule) {
                            Ok(s) => e.cron_id = self.add_job(&mut st, s, key.clone()),
                            Err(err) => e.state.last_error = err,
                        }
                    }
                    st.entries.insert(key, e);
                }
            }
        }
        Ok(())
    }

    /// states lists the triggers with their state.
    pub fn states(&self) -> Vec<TriggerState> {
        let st = self.st.lock();
        let now = self.clock();
        let mut out: Vec<TriggerState> = st
            .entries
            .values()
            .map(|e| {
                let mut s = e.state.clone();
                if e.cron_id != 0
                    && let Some(job) = st.jobs.get(&e.cron_id)
                    && st.running
                {
                    s.next_fire = job.schedule.next(now);
                }
                s
            })
            .collect();
        out.sort_by_key(|s| s.key());
        out
    }

    /// handle fires the event triggers matching ev, and runs the transverse processes alongside the changes it
    /// concerns. Items added to a change raise, after it, the events of the methodologies their signals name (ADR
    /// 0110).
    pub async fn handle(self: &Arc<Self>, ctx: &Ctx, ev: TriggerEvent) {
        let own = declared_events(&ev);
        self.handle_one(ctx, ev).await;
        for o in own {
            self.handle_one(ctx, o).await;
        }
    }

    async fn handle_one(self: &Arc<Self>, ctx: &Ctx, ev: TriggerEvent) {
        self.engine.accompany(ctx, &ev).await;
        if ev.r#type == goap_events::METHODOLOGY_PUBLISHED
            && let Err(err) = self.reload(ctx).await
        {
            tracing::warn!(err = %err, "triggers reload");
        }
        let act = ev.activation();
        let mut keys: Vec<String> = {
            let st = self.st.lock();
            st.entries
                .iter()
                .filter(|(_, e)| {
                    e.state.enabled
                        && e.def.r#type == TRIGGER_EVENT
                        && e.def.event == ev.r#type
                        && e.filter.as_ref().is_none_or(|f| f.matches(&act))
                })
                .map(|(k, _)| k.clone())
                .collect()
        };
        keys.sort();
        for key in keys {
            if let Err(err) = self.fire_key(ctx, &key, Some(&ev)).await
                && !err.is_trigger_skipped()
            {
                tracing::warn!(trigger = %key, event = %ev.r#type, err = %err, "event trigger");
            }
        }
    }

    /// cron_fire is the cron callback for a schedule trigger: it fires only on the leader replica (see leader), so
    /// with several engine replicas sharing the same methodology only one of them starts the process.
    pub(crate) async fn cron_fire(&self, key: &str) {
        if !self.is_leader() {
            return;
        }
        if let Err(err) = self.fire_key(&Ctx::background(), key, None).await {
            tracing::warn!(trigger = %key, err = %err, "scheduled trigger");
        }
    }

    /// fire fires a trigger manually (the event of event triggers is absent).
    pub async fn fire(
        &self,
        ctx: &Ctx,
        methodology: &str,
        agent: &str,
        name: &str,
    ) -> Result<Process> {
        self.fire_key(ctx, &format!("{methodology}/{agent}/{name}"), None)
            .await
    }

    async fn fire_key(&self, ctx: &Ctx, key: &str, ev: Option<&TriggerEvent>) -> Result<Process> {
        let (def, state) = {
            let mut st = self.st.lock();
            let Some(e) = st.entries.get_mut(key) else {
                return Err(Error::UnknownTrigger(format!("{key}: unknown trigger")));
            };
            let min_interval = if self.min_interval.is_zero() {
                Duration::from_secs(2)
            } else {
                self.min_interval
            };
            // loop protection: never react to the output of this trigger
            if let Some(ev) = ev {
                let own_process = ev.process.as_ref().is_some_and(|p| p.trigger == key);
                let own_change = ev
                    .change
                    .as_ref()
                    .is_some_and(|c| c.data.get("trigger").and_then(Value::as_str) == Some(key));
                if own_process || own_change {
                    return Err(Error::TriggerSkipped);
                }
                // a superseding trigger is not rate limited: the latest event is the one that counts
                if !e.def.supersede
                    && let Some(last) = e.state.last_fired
                    && (self.clock() - last).to_std().unwrap_or_default() < min_interval
                {
                    tracing::warn!(trigger = %key, "trigger rate limited");
                    return Err(Error::TriggerSkipped);
                }
            }
            let (def, state) = (e.def.clone(), e.state.clone());
            e.state.last_fired = Some(self.clock());
            e.state.fires += 1;
            (def, state)
        };
        let res = self.start_run(ctx, key, &state, &def, ev).await;
        let mut st = self.st.lock();
        if let Some(cur) = st.entries.get_mut(key) {
            cur.state.last_error = String::new();
            match &res {
                Err(err) => cur.state.last_error = err.to_string(),
                Ok(p) => cur.state.last_process_id = p.id.clone(),
            }
        }
        res
    }

    async fn start_run(
        &self,
        ctx: &Ctx,
        key: &str,
        s: &TriggerState,
        def: &Trigger,
        ev: Option<&TriggerEvent>,
    ) -> Result<Process> {
        let roles: Vec<&str> = def.roles.iter().map(String::as_str).collect();
        let mut who = goap_authz::system(&format!("trigger:{key}"), &roles);
        who.org = "system".into();
        if let Some(ev) = ev {
            who = who.for_user(&ev.end_user_of()); // its run is a consequence of what the end user did (ADR 0114)
            if let Some(p) = &ev.process
                && !p.initiator.org.is_empty()
            {
                who.org = p.initiator.org.clone();
            }
        }
        let ctx = goap_authz::with_principal(ctx, who.clone());
        let mut req = StartRequest {
            methodology: s.methodology.clone(),
            agent: s.agent.clone(),
            goal: def.goal.clone(),
            intent: def.intent.clone(),
            trigger: key.to_string(),
            title: format!("{} (trigger {})", s.agent, s.name),
            ..Default::default()
        };
        if req.intent.is_empty() {
            req.intent = format!(
                "Automatic run: {}",
                format!("{} {}", s.name, def.description).trim()
            );
        }
        if let Some(ev) = ev {
            if let Some(p) = &ev.process {
                req.cause = p.id.clone();
            }
            // the event is available to the agent (conditions, scripts, builtins)
            req.vars
                .insert("event".into(), Value::Object(ev.activation()));
        }
        if def.target == TARGET_EVENT_CHANGE {
            match ev {
                Some(TriggerEvent {
                    change: Some(c), ..
                }) => req.change_id = c.id.clone(),
                Some(TriggerEvent {
                    process: Some(p), ..
                }) if !p.change_id.is_empty() => req.change_id = p.change_id.clone(),
                _ => {
                    return Err(Error::msg(
                        "event_change target needs a change or process event",
                    ));
                }
            }
        } else {
            let m = self
                .engine
                .methodologies
                .methodology(&ctx, &s.methodology)
                .await?;
            req.baseline_id = self
                .engine
                .latest_baseline(&ctx, goap_domain::namespace_of(m.namespace()))
                .await?;
        }
        if def.supersede && !req.change_id.is_empty() {
            self.engine
                .supersede_runs(&ctx, &req.change_id, key, &who.subject)
                .await;
        }
        let p = self.engine.start(&ctx, req).await?;
        tracing::info!(trigger = %key, process = %p.id, status = %p.status, "trigger fired");
        if p.status == STATUS_RUNNING {
            self.engine.schedule_run(&p.id);
        }
        Ok(p)
    }
}

/// declared_events are the events of methodologies (methodology.Event) raised by the signal items of a
/// change.item_added event: one per signal name that is not a standard event, with the signals of that name in
/// items.
pub fn declared_events(ev: &TriggerEvent) -> Vec<TriggerEvent> {
    if ev.r#type != goap_events::CHANGE_ITEM_ADDED {
        return Vec::new();
    }
    let mut out: Vec<TriggerEvent> = Vec::new();
    let mut at: HashMap<String, usize> = HashMap::new();
    for it in &ev.items {
        if it.r#type != TYPE_SIGNAL
            || it.target.is_empty()
            || goap_methodology::standard_event(&it.target)
            || !goap_methodology::EVENT_NAME_RE.is_match(&it.target)
        {
            continue;
        }
        let i = *at.entry(it.target.clone()).or_insert_with(|| {
            out.push(TriggerEvent {
                r#type: it.target.clone(),
                change: ev.change.clone(),
                process: ev.process.clone(),
                end_user: ev.end_user.clone(),
                ..Default::default()
            });
            out.len() - 1
        });
        out[i].items.push(it.clone());
    }
    out
}

impl Engine {
    /// latest_baseline is the latest baseline of a namespace ("" when no change landed yet: the empty state, which
    /// a change then starts from, ADR 0056).
    pub(crate) async fn latest_baseline(&self, ctx: &Ctx, namespace: &str) -> Result<BaselineID> {
        let bs = self.graph.baselines(ctx, namespace).await?;
        let Some(mut latest) = bs.first() else {
            return Ok(BaselineID::default());
        };
        for b in &bs[1..] {
            if b.created_at > latest.created_at {
                latest = b;
            }
        }
        Ok(latest.id.clone())
    }
}
