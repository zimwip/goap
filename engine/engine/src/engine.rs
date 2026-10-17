//! The engine (Go's `engine.go`): processes started, resumed, run cycle after cycle, their actions executed and
//! settled, their human tasks submitted and approved.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::future::Future;
use std::sync::Arc;
use std::sync::atomic::{AtomicI64, AtomicU64, AtomicUsize, Ordering};
use std::time::Duration;

use chrono::{DateTime, Utc};
use futures::future::BoxFuture;
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::{Blackboard, ChangeID, ChangeImpactID, ChangeItem, ItemID};
use goap_goap::WorldState;
use goap_intent::{GoalInfo, Turn};
use goap_journal::Record;
use goap_methodology::{
    Action, Agent, Compiled, Goal, KIND_ABSTRACT, KIND_CALL, KIND_HUMAN, KIND_TOOL,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};
use tokio::sync::Notify;

use crate::cancel::RunHandle;
use crate::companion::StepEvent;
use crate::decisions::DecisionOp;
use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult, ExecError, Executor};
use crate::host::{Host, Sandboxes};
use crate::items::{ItemInput, Resolver};
use crate::journal::{action_record, action_step, end_record};
use crate::locks::{Held, Locker, Signals};
use crate::persistent::INSTANCE_LOCK;
use crate::ports::{GraphPort, MethodologyPort, Payload, Publisher};
use crate::process::{
    HumanTask, LogLine, Process, ProcessLogEntry, Queued, STATUS_CANCELLED, STATUS_CLARIFYING,
    STATUS_COMPLETED, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK, STATUS_WAITING, Store,
    TASK_AGENT, TASK_APPROVAL, TASK_INPUT, TASK_MERGE, TASK_REBASE, Usage,
};
use crate::scope::Scope;
use crate::steps::{CallExecutor, StepContext, context_of_step, step_context};
use crate::tracer::{EndAction, Tracer};

/// The clock of an engine.
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// The type catalogue in force (ADR 0012 §2): the ancestors behind `x.types`.
pub type TypesFn = Arc<dyn Fn() -> Option<Arc<dyn goap_def::TypeSet>> + Send + Sync>;

/// Engine runs processes. Build it with Engine::new, set the fields, then share it as `Arc<Engine>`.
pub struct Engine {
    pub graph: Arc<dyn GraphPort>,
    pub methodologies: Arc<dyn MethodologyPort>,
    /// The executors by action kind (llm, tool, human, builtin, script); call is the engine's own.
    pub executors: HashMap<String, Arc<dyn Executor>>,
    /// The intent loop (None: a lexical ranker).
    pub intent: Option<goap_intent::Resolver>,
    pub store: Arc<dyn Store>,
    pub events: Option<Arc<dyn Publisher>>,
    pub planner: goap_goap::Planner,
    /// Who and where (ADR 0063). None: an AuthzScope granting everything and binding no MCP.
    pub scope: Option<Arc<dyn Scope>>,
    /// Serves DSL model calls (the model gateway).
    pub llm: Option<Arc<dyn goap_llm::Client>>,
    /// Isolates script actions (one sandbox per process run).
    pub sandboxes: Option<Arc<dyn Sandboxes>>,
    /// Instruments processes and actions (None: no tracing).
    pub tracer: Option<Arc<dyn Tracer>>,
    /// Runs a process in the background (default: a tokio task).
    pub schedule: Option<Arc<dyn Fn(String) + Send + Sync>>,
    /// Bounds the number of actions of a process (default 50).
    pub max_steps: i64,
    /// The oversight floor of the platform (EU AI Act Art. 14, oversight.rs).
    pub person_reviews_model_output: bool,
    /// Bounds the subjects a scoped action runs for at once (default 4).
    pub max_parallel: i64,
    /// Disables an action after that many executions without the promised effects (default 1, ADR 0112 §6).
    pub max_failures: i64,
    /// The type catalogue in force (None, or returning None: the methodology's own resolved types).
    pub types: Option<TypesFn>,
    /// The lock service shared by the engine replicas (ADR 0124). None: one replica.
    pub locks: Option<Arc<dyn Locker>>,
    /// The lease of a shared lock (zero: DEFAULT_LOCK_TTL).
    pub lock_ttl: Duration,
    /// Bounds the runs this replica holds at once with shared locks (0: no bound).
    pub max_runs: i64,
    /// Carries the wake-ups and interruptions between replicas.
    pub signals: Option<Arc<dyn Signals>>,

    pub(crate) key_locks: parking_lot::Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>,
    pub(crate) recovering: parking_lot::Mutex<HashSet<String>>,
    pub(crate) held: parking_lot::Mutex<HashMap<String, Arc<Held>>>,
    pub(crate) inflight: AtomicI64,
    pub(crate) wake: Notify,
    pub(crate) running: parking_lot::Mutex<HashMap<String, RunHandle>>,
    pub(crate) cancelling: parking_lot::Mutex<HashMap<String, String>>,
    pub(crate) run_seq: AtomicU64,
    pub(crate) now: parking_lot::RwLock<Option<Clock>>,
    bg: Arc<BgState>,
}

impl Engine {
    /// An engine over a graph, a methodology registry and a process store, with the defaults of Go's zero Engine.
    pub fn new(
        graph: Arc<dyn GraphPort>,
        methodologies: Arc<dyn MethodologyPort>,
        store: Arc<dyn Store>,
    ) -> Engine {
        Engine {
            graph,
            methodologies,
            executors: HashMap::new(),
            intent: None,
            store,
            events: None,
            planner: goap_goap::Planner::default(),
            scope: None,
            llm: None,
            sandboxes: None,
            tracer: None,
            schedule: None,
            max_steps: 0,
            person_reviews_model_output: false,
            max_parallel: 0,
            max_failures: 0,
            types: None,
            locks: None,
            lock_ttl: Duration::ZERO,
            max_runs: 0,
            signals: None,
            key_locks: parking_lot::Mutex::new(HashMap::new()),
            recovering: parking_lot::Mutex::new(HashSet::new()),
            held: parking_lot::Mutex::new(HashMap::new()),
            inflight: AtomicI64::new(0),
            wake: Notify::new(),
            running: parking_lot::Mutex::new(HashMap::new()),
            cancelling: parking_lot::Mutex::new(HashMap::new()),
            run_seq: AtomicU64::new(0),
            now: parking_lot::RwLock::new(None),
            bg: Arc::new(BgState::default()),
        }
    }

    /// set_clock replaces the clock of the engine (tests).
    pub fn set_clock(&self, c: Option<Clock>) {
        *self.now.write() = c;
    }

    /// background runs f in a tracked task (see drain).
    pub(crate) fn background(&self, f: impl Future<Output = ()> + Send + 'static) {
        self.bg.count.fetch_add(1, Ordering::SeqCst);
        let counter = BgGuard(self.bg.clone());
        tokio::spawn(async move {
            let _counter = counter;
            f.await;
        });
    }

    /// drain waits for the background work (tests).
    pub async fn drain(&self) {
        loop {
            let notified = self.bg.done.notified();
            if self.bg.count.load(Ordering::SeqCst) == 0 {
                return;
            }
            notified.await;
        }
    }

    pub(crate) fn schedule_run(self: &Arc<Self>, id: &str) {
        if let Some(s) = &self.schedule {
            s(id.to_string());
            return;
        }
        if self.locks.is_some() && self.at_capacity() {
            self.leave(id);
            return;
        }
        let e = self.clone();
        let id = id.to_string();
        self.background(async move { e.run_counted(&id).await });
    }

    /// queue notes why p is runnable: the run that picks it up journals it as a schedule record.
    pub(crate) fn queue(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        reason: &str,
        cause: Option<Map<String, Value>>,
    ) {
        let mut by = goap_authz::principal_from(ctx).subject;
        if by.is_empty() {
            by = p.initiator.subject.clone();
        }
        p.queued = Some(Queued {
            reason: reason.to_string(),
            by,
            at: self.clock(),
            cause: cause.unwrap_or_default(),
        });
    }

    pub(crate) fn clock(&self) -> DateTime<Utc> {
        if let Some(c) = &*self.now.read() {
            return c();
        }
        Utc::now()
    }
}

/// The background tasks of an engine (drain).
#[derive(Default)]
struct BgState {
    count: AtomicUsize,
    done: Notify,
}

/// The counter of a background task: its drop marks the task done.
struct BgGuard(Arc<BgState>);

impl Drop for BgGuard {
    fn drop(&mut self) {
        if self.0.count.fetch_sub(1, Ordering::SeqCst) == 1 {
            self.0.done.notify_waiters();
        }
    }
}

/// StartRequest starts a process.
#[derive(Debug, Clone, Default)]
pub struct StartRequest {
    pub methodology: String,
    /// Continues an existing change; otherwise a change is opened on baseline_id.
    pub change_id: ChangeID,
    pub baseline_id: goap_domain::BaselineID,
    /// Namespace of the new change.
    pub namespace: String,
    /// Gives the new change a branch of its own.
    pub own_branch: bool,
    /// The key of the OrgUnit holding the new change (empty: the default organisation).
    pub owner_org: String,
    /// The key of the ProjectUnit the new change belongs to (empty: the root project).
    pub project_id: String,
    pub title: String,
    pub intent: String,
    /// Skips the intent loop (with agent, or the first agent having it).
    pub goal: String,
    /// Restricts identification to one agent of the methodology.
    pub agent: String,
    /// Set for sub-agent processes; call names the call of the parent's action that started it.
    pub parent_id: String,
    pub call: String,
    /// Set for processes started by a trigger.
    pub trigger: String,
    /// The process whose event fired the trigger.
    pub cause: String,
    pub vars: Map<String, Value>,
    /// The step of the parent's process a sub-agent carries out (ADR 0034).
    pub step: Option<StepContext>,
    /// Puts the process on an already-open flow of change_id (ADR 0017); "" is the main flow.
    pub flow: String,
}

/// target is an identifiable (methodology, agent, goal) triple.
struct Target {
    m: Arc<Compiled>,
    agent: Agent,
    goal: Goal,
}

impl Target {
    fn key(&self) -> String {
        format!("{}/{}/{}", self.m.name(), self.agent.name, self.goal.name)
    }
}

/// AttachRequest binds a process to a change: an existing one by change_id, or a new one.
#[derive(Debug, Clone, Default)]
pub struct AttachRequest {
    pub change_id: ChangeID,
    pub title: String,
    pub intent: String,
    pub namespace: String,
    pub owner_org: String,
    pub project_id: String,
    pub baseline_id: goap_domain::BaselineID,
}

/// first_non_empty is the first non-empty string.
pub(crate) fn first_non_empty(v: &[&str]) -> String {
    v.iter()
        .find(|s| !s.is_empty())
        .map(|s| s.to_string())
        .unwrap_or_default()
}

pub(crate) fn first_user_turn(p: &Process) -> String {
    p.intent
        .turns
        .iter()
        .find(|t| t.role == "user")
        .map(|t| t.text.clone())
        .unwrap_or_default()
}

/// ran is what the executor of an action returned, to be settled on the process (settle_action).
pub(crate) struct Ran {
    pub(crate) ctx: Ctx,
    pub(crate) end: EndAction,
    pub(crate) host: Arc<Host>,
    pub(crate) sc: Option<StepContext>,
    pub(crate) res: ActionResult,
    pub(crate) err: Option<Error>,
}

/// PERMISSION_RUN_ACTION is the permission of an approval an action waits for when its initiator holds none of the
/// roles allowed to run it (ADR 0043).
pub const PERMISSION_RUN_ACTION: &str = "action:run";

/// VAR_CONVERSATION is the process variable naming the assistant conversation that started it (ADR 0090).
pub const VAR_CONVERSATION: &str = "conversationId";

/// conversation_of is the conversation of the assistant that started the process (ADR 0090), "" for any other.
pub(crate) fn conversation_of(p: &Process) -> String {
    p.vars
        .get(VAR_CONVERSATION)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

/// ProcessEvent is published on every process save, and for log lines.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProcessEvent {
    pub event: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub process: Option<Process>,
    /// The step of a process that completed (event "step_completed", ADR 0036 §3).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub step: Option<StepEvent>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub log: Option<LogLine>,
    #[serde(with = "goap_domain::gotime")]
    pub time: DateTime<Utc>,
}

fn event_of(p: &Process) -> String {
    if p.status == STATUS_RUNNING {
        "step".to_string()
    } else {
        p.status.to_string()
    }
}

/// truncate cuts s to n runes, the last one an ellipsis.
pub(crate) fn truncate(s: &str, n: usize) -> String {
    let r: Vec<char> = s.chars().collect();
    if r.len() <= n {
        return s.to_string();
    }
    let mut out: String = r[..n.saturating_sub(1)].iter().collect();
    out.push('…');
    out
}

pub(crate) fn item_ids(items: &[ChangeItem]) -> Vec<ItemID> {
    items.iter().map(|it| it.id.clone()).collect()
}

fn all_bound(a: &Action, bound: &BTreeMap<String, bool>) -> bool {
    for n in a.required_mcps() {
        if !bound.get(&n).copied().unwrap_or(false) {
            return false;
        }
    }
    // a tool action also needs its tool, which the unit may have restricted
    a.kind != KIND_TOOL || bound.get(&a.tool).copied().unwrap_or(false)
}

/// agent_binds_own_change reports whether the agent declares its own action to bind a change (an action whose
/// effects establish change_bound, ADR 0031).
fn agent_binds_own_change(m: &Compiled, ag: &Agent) -> bool {
    m.agent_actions(ag)
        .iter()
        .any(|a| a.effects.get("change_bound").copied().unwrap_or(false))
}

impl Engine {
    /// start creates a process and resolves its intent. The returned process is either clarifying (a question is
    /// pending) or running (call run). Without methodology, identification ranks the agents of every published
    /// methodology.
    pub fn start<'a>(
        self: &'a Arc<Self>,
        ctx: &'a Ctx,
        req: StartRequest,
    ) -> BoxFuture<'a, Result<Process>> {
        Box::pin(async move {
            let (mut key, _) = self.instance_key(ctx, &req).await?;
            let mut _unlock = None;
            if !key.is_empty() {
                // a persistent agent (ADR 0112): the execution goes to its instance on the scope, if one is open
                _unlock = Some(self.lock(&format!("{INSTANCE_LOCK}{key}")).await);
                let (p, handled, fresh) = self.resume_instance(ctx, &key, &req).await?;
                if handled {
                    return p.ok_or_else(|| Error::msg("persistent instance: no process"));
                }
                if fresh {
                    key = String::new();
                }
            }
            self.start_inner(ctx, req, &key).await
        })
    }

    /// start_inner creates a process (the instance of key when it is not empty) and resolves its intent.
    pub(crate) async fn start_inner(
        self: &Arc<Self>,
        ctx: &Ctx,
        req: StartRequest,
        instance: &str,
    ) -> Result<Process> {
        let now = self.clock();
        let mut p = Process {
            instance: instance.to_string(),
            id: uuid::Uuid::new_v4().to_string(),
            methodology: req.methodology.clone(),
            agent: req.agent.clone(),
            change_id: req.change_id.clone(),
            baseline_id: req.baseline_id.clone(),
            namespace: req.namespace.clone(),
            own_branch: req.own_branch,
            org: req.owner_org.clone(),
            project: req.project_id.clone(),
            title: req.title.clone(),
            parent_id: req.parent_id.clone(),
            trigger: req.trigger.clone(),
            cause: req.cause.clone(),
            flow: req.flow.clone(),
            initiator: goap_authz::principal_from(ctx),
            vars: req.vars.clone(),
            step: req.step.clone(),
            disabled: BTreeMap::new(),
            created_at: now,
            updated_at: now,
            ..Default::default()
        };
        if p.initiator.is_system() && p.initiator.on_behalf_of.is_empty() {
            // a platform service starting a sub-agent or a consequence of a run acts for that run's end user
            for from in [&req.parent_id, &req.cause] {
                if from.is_empty() {
                    continue;
                }
                if let Ok(q) = self.store.get(ctx, from).await {
                    let user = q.initiator.end_user().to_string();
                    p.initiator = p.initiator.clone().for_user(&user);
                    break;
                }
            }
        }
        if !req.intent.is_empty() {
            self.append_intent_turns(ctx, &mut p, vec![Turn::new("user", req.intent.clone())])
                .await?;
        }
        if !req.change_id.is_empty() {
            let bb = self.graph.blackboard(ctx, &req.change_id).await?;
            if req.baseline_id.is_empty() {
                p.baseline_id = bb.change.baseline_id.clone();
            }
            // a run works where its change does (ADR 0091)
            p.project = bb.change.project_id.clone();
            // and with the version of the methodology its change follows (ADR 0113)
            if !p.methodology.is_empty() && p.methodology == bb.change.methodology {
                p.methodology_version = bb.change.methodology_version.clone();
            }
        }
        if !req.goal.is_empty() {
            let m = self.methodology_of(ctx, &p).await?;
            if m.goal(&req.goal).is_none() {
                return Err(Error::msg(format!("unknown goal {:?}", req.goal)));
            }
            if p.step.is_none()
                && req.goal.contains('/')
                && let Some(si) = m.step_by_path(&req.goal)
            {
                p.step = Some(context_of_step(&si)); // a run towards the goal of a step carries that step out
            }
            let mut agent = req.agent.clone();
            if agent.is_empty() {
                for ag in m.agent_list() {
                    if m.agent_goals(&ag).iter().any(|g| g.name == req.goal) {
                        agent = ag.name.clone();
                        break;
                    }
                }
            }
            self.select_target(ctx, &mut p, &m, &agent, &req.goal)
                .await?;
        } else {
            self.resolve_intent(ctx, &mut p).await?;
        }
        if p.status == STATUS_RUNNING {
            self.check_agent_roles(ctx, &p).await?;
            if !p.parent_id.is_empty() {
                let cause = obj(json!({"parent": p.parent_id, "call": req.call}));
                self.queue(ctx, &mut p, "sub-agent", Some(cause));
            } else if !p.trigger.is_empty() {
                let cause = obj(json!({"trigger": p.trigger}));
                self.queue(ctx, &mut p, "trigger", Some(cause));
            } else {
                self.queue(ctx, &mut p, "started", None);
            }
        }
        self.save(ctx, &mut p, "started").await?;
        Ok(p)
    }

    /// answer adds a user answer to a clarifying process and resolves again.
    pub async fn answer(self: &Arc<Self>, ctx: &Ctx, id: &str, answer: &str) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_CLARIFYING {
            return Err(Error::msg(format!(
                "process {id} is {}, not clarifying",
                p.status
            )));
        }
        self.append_intent_turns(ctx, &mut p, vec![Turn::new("user", answer)])
            .await?;
        self.resolve_intent(ctx, &mut p).await?;
        if p.status == STATUS_RUNNING {
            self.queue(ctx, &mut p, "answered", None);
        }
        self.save(ctx, &mut p, "intent").await?;
        Ok(p)
    }

    /// append_intent_turns adds turns to the in-memory session and logs them (ADR 0031).
    async fn append_intent_turns(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        turns: Vec<Turn>,
    ) -> Result<()> {
        p.intent.turns.extend(turns.iter().cloned());
        self.log_intent_turns(ctx, &p.id, &turns).await
    }

    pub(crate) async fn log_turns(
        &self,
        ctx: &Ctx,
        process_id: &str,
        turns: &[Turn],
    ) -> Result<()> {
        self.log_intent_turns(ctx, process_id, turns).await
    }

    async fn log_intent_turns(&self, ctx: &Ctx, process_id: &str, turns: &[Turn]) -> Result<()> {
        let entries = turns
            .iter()
            .map(|t| ProcessLogEntry {
                r#type: "intent.turn".into(),
                at: self.clock(),
                payload: obj(json!({"role": t.role, "text": t.text})),
                ..Default::default()
            })
            .collect();
        self.store
            .append_process_log(ctx, process_id, entries)
            .await
    }

    /// methodology_of is the methodology a process runs, at the version it started with (ADR 0113).
    pub(crate) async fn methodology_of(&self, ctx: &Ctx, p: &Process) -> Result<Arc<Compiled>> {
        self.methodologies
            .methodology(
                ctx,
                &goap_domain::methodology_ref(&p.methodology, &p.methodology_version),
            )
            .await
    }

    async fn targets(&self, ctx: &Ctx, p: &Process) -> Result<Vec<Target>> {
        let ms = if !p.methodology.is_empty() {
            vec![self.methodology_of(ctx, p).await?]
        } else {
            self.methodologies.list(ctx).await?
        };
        let mut out = Vec::new();
        for m in &ms {
            for ag in m.agent_list() {
                if !p.agent.is_empty() && ag.name != p.agent {
                    continue;
                }
                for g in m.agent_goals(&ag) {
                    out.push(Target {
                        m: m.clone(),
                        agent: ag.clone(),
                        goal: g,
                    });
                }
            }
        }
        if out.is_empty() {
            return Err(Error::invalid_state(format!(
                "no agent goal matches methodology {:?} agent {:?}",
                p.methodology, p.agent
            )));
        }
        Ok(out)
    }

    /// resolve_intent identifies the (methodology, agent, goal) of the intent.
    async fn resolve_intent(self: &Arc<Self>, ctx: &Ctx, p: &mut Process) -> Result<()> {
        let ts = self.targets(ctx, p).await?;
        let mut by_key: HashMap<String, &Target> = HashMap::new();
        let mut goals = Vec::with_capacity(ts.len());
        for t in &ts {
            by_key.insert(t.key(), t);
            let mut desc = t.goal.description.clone();
            if !t.agent.description.is_empty() && t.agent.description != t.m.description() {
                desc = format!("{} — {}", t.agent.description, desc);
            }
            let mut examples = t.agent.examples.clone();
            examples.extend(t.goal.examples.iter().cloned());
            goals.push(GoalInfo {
                name: t.key(),
                description: desc,
                examples,
            });
        }
        let before = p.intent.turns.len();
        let resolver = match &self.intent {
            Some(r) => r.clone(),
            None => goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)),
        };
        let res = resolver
            .resolve(ctx, &mut p.intent, &goals)
            .await
            .map_err(|e| Error::msg(e.to_string()))?;
        let added: Vec<Turn> = p.intent.turns[before..].to_vec();
        if !added.is_empty() {
            self.log_intent_turns(ctx, &p.id, &added).await?;
        }
        p.candidates = Vec::new();
        for mut c in res.candidates.clone() {
            if let Some(t) = by_key.get(&c.goal) {
                c.methodology = t.m.name().to_string();
                c.agent = t.agent.name.clone();
                c.goal = t.goal.name.clone();
            }
            p.candidates.push(c);
        }
        if !res.resolved() {
            p.status = STATUS_CLARIFYING.into();
            p.question = res.question.clone();
            return Ok(());
        }
        let Some(t) = by_key.get(&res.goal) else {
            return Err(Error::msg(format!("unknown goal {:?}", res.goal)));
        };
        let (m, agent, goal) = (t.m.clone(), t.agent.name.clone(), t.goal.name.clone());
        self.select_target(ctx, p, &m, &agent, &goal).await
    }

    /// select_target fixes the methodology, agent and goal and, unless the agent declares its own action to bind one,
    /// opens the change eagerly.
    async fn select_target(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        agent_name: &str,
        goal: &str,
    ) -> Result<()> {
        let agent_name = if agent_name.is_empty() {
            m.agent_list()
                .first()
                .map(|a| a.name.clone())
                .unwrap_or_default()
        } else {
            agent_name.to_string()
        };
        let Some(ag) = m.agent(&agent_name).cloned() else {
            return Err(Error::invalid_state(format!(
                "unknown agent {agent_name:?} in {}",
                m.name()
            )));
        };
        p.methodology = m.name().to_string();
        p.agent = ag.name.clone();
        p.planner = ag.planner.clone();
        p.goal = goal.to_string();
        // the version the process runs from now on (ADR 0113)
        p.methodology_version = m.version().to_string();
        p.question = String::new();
        p.status = STATUS_RUNNING.into();
        if p.change_id.is_empty() && !agent_binds_own_change(m, &ag) {
            self.bind_change(ctx, p, m, AttachRequest::default())
                .await?;
        }
        Ok(())
    }

    /// attach_change binds a standalone (not-yet-bound) process to a change (ADR 0031, gap 5).
    pub async fn attach_change(
        self: &Arc<Self>,
        ctx: &Ctx,
        process_id: &str,
        req: AttachRequest,
    ) -> Result<()> {
        let _u = self.lock(process_id).await;
        let mut p = self.store.get(ctx, process_id).await?;
        let m = self.methodology_of(ctx, &p).await?;
        self.bind_change(ctx, &mut p, &m, req).await?;
        self.schedule_run(&p.id);
        Ok(())
    }

    /// bind_change resolves and binds the change of an already-locked process.
    async fn bind_change(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        req: AttachRequest,
    ) -> Result<()> {
        let id = if req.change_id.is_empty() {
            self.resolve_change(ctx, p, m, &req).await?
        } else {
            self.graph
                .blackboard(ctx, &req.change_id)
                .await
                .map_err(|e| Error::from(e).context(format!("attach {}", req.change_id)))?;
            req.change_id.clone()
        };
        p.change_id = id.clone();
        let rec = Record {
            kind: goap_journal::KIND_ATTACH.into(),
            step: p.steps.len() as i64,
            data: obj(json!({"changeId": id.to_string(), "reused": !req.change_id.is_empty()})),
            ..Default::default()
        };
        self.journal(ctx, p, vec![rec]).await;
        self.save(ctx, p, goap_events::BROKER_ATTACHED).await
    }

    /// resolve_change creates a new change for p, defaulting from req where given and from p / its methodology
    /// otherwise.
    pub(crate) async fn resolve_change(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        req: &AttachRequest,
    ) -> Result<ChangeID> {
        let mut title = first_non_empty(&[&req.title, &p.title]);
        if title.is_empty() {
            title = truncate(&first_user_turn(p), 80);
        }
        if title.is_empty() {
            title = format!("{} / {}", m.name(), p.goal);
        }
        p.title = title.clone();
        let fut = first_user_turn(p);
        let intent = first_non_empty(&[&req.intent, &fut]);
        let owner_org = first_non_empty(&[&req.owner_org, &p.org]);
        let mut project_id = first_non_empty(&[&req.project_id, &p.project]);
        if project_id.is_empty() {
            let st = self.graph.structures(ctx).await?;
            project_id = st.project().root.clone();
        }
        let mut data = Map::new();
        if !p.trigger.is_empty() {
            data.insert("trigger".into(), Value::String(p.trigger.clone()));
        }
        if m.administrative() {
            data.insert(goap_domain::DATA_ADMINISTRATIVE.into(), Value::Bool(true));
        }
        if !m.criticality().is_empty() {
            data.insert(
                goap_domain::DATA_CRITICALITY.into(),
                Value::String(m.criticality().to_string()),
            );
        }
        let ns = first_non_empty(&[&req.namespace, &p.namespace, m.namespace()]);
        let mut baseline = req.baseline_id.clone();
        if baseline.is_empty() {
            baseline = p.baseline_id.clone();
        }
        if baseline.is_empty() {
            baseline = self
                .latest_baseline(ctx, goap_domain::namespace_of(&ns))
                .await
                .map_err(|e| e.context(format!("methodology {}", m.name())))?;
        }
        p.baseline_id = baseline.clone();
        let c = self
            .graph
            .create_change(
                ctx,
                goap_graphapi::NewChange {
                    title,
                    intent,
                    methodology: m.name().to_string(),
                    owner_org,
                    project_id,
                    namespace: ns,
                    own_branch: p.own_branch,
                    baseline_id: baseline,
                    data,
                    ..Default::default()
                },
            )
            .await?;
        if p.project.is_empty() {
            p.project = c.project_id.clone();
        }
        Ok(c.id)
    }

    /// submit provides the items of a pending human task and resumes the process (call run afterwards).
    pub async fn submit(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        items: Vec<ItemInput>,
    ) -> Result<Process> {
        self.submit_inner(ctx, id, items, None).await
    }

    /// submit_form provides the values of the form of a pending human task (ADR 0115).
    pub async fn submit_form(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        values: Map<String, Value>,
    ) -> Result<Process> {
        self.submit_inner(ctx, id, Vec::new(), Some(values)).await
    }

    async fn submit_inner(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        mut items: Vec<ItemInput>,
        values: Option<Map<String, Value>>,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_WAITING || p.pending.as_ref().is_none_or(|t| t.kind != TASK_INPUT) {
            return Err(Error::invalid_state(format!(
                "process {id} has no pending human task"
            )));
        }
        let m = self.methodology_of(ctx, &p).await?;
        let who = goap_authz::principal_from(ctx);
        self.may_submit(ctx, &p, &m, &who).await?;
        let pending = p.pending.clone().unwrap_or_default();
        let action = m.action(&pending.action).cloned();
        let i = pending.step as usize;
        let waited_on = pending.action.clone();
        if i >= p.steps.len() {
            return Err(Error::invalid_state(format!(
                "process {id} has no step {i}"
            )));
        }
        let rec = uuid::Uuid::new_v4().to_string();
        let submitted = self.clock();
        // the submission is the run of the step (ADR 0034, 0109)
        p.steps[i].execution = rec.clone();
        let mut ops = Vec::new();
        if let Some(values) = values {
            let Some(action) = action.as_ref().filter(|a| a.form.is_some()) else {
                return Err(Error::invalid_state(format!(
                    "action {} declares no form",
                    pending.action
                )));
            };
            let (its, os) = self.form_items(ctx, &mut p, &m, action, i, values).await?;
            items = its;
            ops = os;
        }
        let by = first_non_empty(&[&who.subject, &pending.action]);
        let (ids, mut nodes, points) = self
            .add_items(ctx, &mut p, items, &pending.action, &rec, true, &by)
            .await?;
        if !ops.is_empty() {
            let more = self
                .apply_node_ops(ctx, &p, ops, &pending.action, &rec)
                .await?;
            nodes.extend(more);
        }
        p.steps[i].items = ids.clone();
        p.steps[i].nodes = nodes;
        p.steps[i].decisions = points;
        self.finish_step(ctx, &mut p, &m, i).await?;
        p.pending = None;
        p.status = STATUS_RUNNING.into();
        let cause = obj(json!({"step": i, "action": waited_on}));
        self.queue(ctx, &mut p, "input", Some(cause));
        let mut r = action_record(&p, i, KIND_HUMAN, &action_step(&m, &waited_on), &rec);
        r.actor = who.subject.clone();
        r.started_at = Some(submitted);
        r.data = obj(json!({"submitted": ids.len()}));
        self.journal(ctx, &mut p, vec![r]).await;
        self.save(ctx, &mut p, "step").await?;
        Ok(p)
    }

    /// run executes cycles until the process completes, blocks or fails.
    pub fn run<'a>(
        self: &'a Arc<Self>,
        ctx: &'a Ctx,
        id: &'a str,
    ) -> BoxFuture<'a, Result<Process>> {
        Box::pin(async move {
            let _u = self.lock(id).await;
            // the run can be interrupted by cancel: its model and tool calls end with the context
            let (ctx, cancel) = ctx.with_cancel();
            let seq = self.run_seq.fetch_add(1, Ordering::SeqCst);
            self.running.lock().insert(
                id.to_string(),
                RunHandle {
                    cancel: cancel.clone(),
                    id: seq,
                },
            );
            let res = self.run_locked(&ctx, id).await;
            {
                let mut running = self.running.lock();
                if running.get(id).is_some_and(|h| h.id == seq) {
                    running.remove(id);
                }
            }
            cancel.cancel();
            res
        })
    }

    async fn run_locked(self: &Arc<Self>, ctx: &Ctx, id: &str) -> Result<Process> {
        let mut p = self.store.get(ctx, id).await?;
        let m = self.methodology_of(ctx, &p).await?;
        // a run scheduled in the background acts for the principal who started the process (ADR 0029)
        let ctx = if goap_authz::principal_from(ctx).anonymous() {
            self.as_(ctx, &p)
        } else {
            ctx.clone()
        };
        let (ctx, end) = self.tracer().start_process(&ctx, &p);
        let res = self.run_loop(&ctx, &mut p, &m).await;
        end(&p);
        res.map(|()| p)
    }

    async fn run_loop(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
    ) -> Result<()> {
        p.methodology_version = m.version().to_string(); // the version it started with (ADR 0113)
        if self.locks.is_some() {
            // steps a stopped replica left in flight are closed before the run goes on (ADR 0124)
            self.settle_interrupted(ctx, p, m).await?;
        }
        self.journal_started(ctx, p).await;
        if p.status == STATUS_RUNNING
            && let Some(q) = p.queued.clone()
        {
            // why this run happens: what made the process runnable, by whom, and how long it waited
            let mut data = Map::new();
            data.insert("reason".into(), Value::String(q.reason.clone()));
            data.extend(q.cause.clone());
            let rec = Record {
                kind: goap_journal::KIND_SCHEDULE.into(),
                step: p.steps.len() as i64,
                actor: q.by.clone(),
                started_at: Some(q.at),
                ended_at: Some(self.clock()),
                data,
                ..Default::default()
            };
            self.journal(ctx, p, vec![rec]).await;
            p.queued = None;
        }
        let max_steps = if self.max_steps == 0 {
            50
        } else {
            self.max_steps
        };
        while p.status == STATUS_RUNNING {
            if self.cancel_requested(&p.id) {
                return Ok(()); // cancel ends the process once the run let go of it
            }
            if let Some(err) = ctx.err() {
                return Err(err.into());
            }
            if crate::scoped::counted(&p.steps) >= max_steps {
                self.fail(p, &Error::msg(format!("step limit {max_steps} reached")));
                break;
            }
            // fallback for a deferred process that only attaches mid-loop
            self.journal_started(ctx, p).await;
            if let Err(err) = self.cycle(ctx, p, m).await
                && !self.cancel_requested(&p.id)
            {
                self.fail(p, &err);
            }
            let ev = event_of(p);
            self.save(ctx, p, &ev).await?;
        }
        Ok(())
    }

    /// journal_started writes process.started the first time a process has a change to journal to.
    async fn journal_started(&self, ctx: &Ctx, p: &mut Process) {
        if p.started || p.change_id.is_empty() {
            return;
        }
        let rec = Record {
            kind: goap_journal::KIND_PROCESS_STARTED.into(),
            started_at: Some(p.created_at),
            data: obj(
                json!({"intent": first_user_turn(p), "trigger": p.trigger, "title": p.title}),
            ),
            ..Default::default()
        };
        self.journal(ctx, p, vec![rec]).await;
        p.started = true;
    }

    pub(crate) fn fail(&self, p: &mut Process, err: &Error) {
        p.status = STATUS_FAILED.into();
        p.error = err.to_string();
        tracing::error!(process = %p.id, err = %err, "process failed");
    }

    /// execute runs an action for the step at index i, records its outcome and journals it.
    pub(crate) async fn execute(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        bb: &Blackboard,
        action: &Action,
        i: usize,
    ) -> Result<()> {
        let id = uuid::Uuid::new_v4().to_string();
        p.steps[i].execution = id.clone();
        self.checkpoint(ctx, p).await?;
        let (imp, spec) = match self.specialize(ctx, p, m, action, bb).await {
            Ok(v) => v,
            Err(err) => {
                let now = self.clock();
                p.steps[i].error = err.to_string();
                p.steps[i].ended_at = Some(now);
                let rec = action_record(p, i, &action.kind, &action.step, &id);
                self.journal(ctx, p, vec![rec]).await;
                p.steps[i].drop_exchanges();
                self.record_failure(p, &action.name);
                return Ok(());
            }
        };
        p.steps[i].specialization = spec;
        let res = self.execute_step(ctx, p, m, bb, &imp, i).await;
        let rec = action_record(p, i, &imp.kind, &action.step, &id);
        self.journal(ctx, p, vec![rec]).await;
        p.steps[i].drop_exchanges();
        res
    }

    /// bound_mcps returns the MCPs the organization of the process binds for actions and the tools they may call.
    pub(crate) async fn bound_mcps(
        &self,
        ctx: &Ctx,
        p: &Process,
    ) -> Result<BTreeMap<String, bool>> {
        let mut bound = BTreeMap::new();
        let r = self.r#ref(p);
        let (tools, names) = self
            .scope()
            .tools(ctx, &r)
            .await
            .map_err(|e| e.context(format!("MCPs of organization {}", r.org)))?;
        // an MCP of scope agent is not the business of an action (ADR 0028)
        let mut agent_only = HashSet::new();
        for t in &tools {
            if !goap_mcp::for_actions(&t.scope) {
                if let Ok((m, _)) = goap_mcp::split_tool(&t.name) {
                    agent_only.insert(m);
                }
                continue;
            }
            bound.insert(t.name.clone(), true);
        }
        for n in names {
            let ok = !agent_only.contains(&n);
            bound.insert(n, ok);
        }
        Ok(bound)
    }

    /// schedulable reports whether every MCP the action needs is bound by the organization (None: nothing to check).
    pub(crate) fn schedulable(
        &self,
        m: &Compiled,
        name: &str,
        bound: Option<&BTreeMap<String, bool>>,
    ) -> bool {
        let Some(bound) = bound else {
            return true;
        };
        match m.action(name) {
            Some(a) => all_bound(a, bound),
            None => true,
        }
    }

    /// specialize returns the implementation to run for a planned action: the applicable specialization with the
    /// highest priority (declared in this methodology, then in the others), else the action itself.
    pub(crate) async fn specialize(
        &self,
        ctx: &Ctx,
        p: &Process,
        m: &Arc<Compiled>,
        action: &Action,
        bb: &Blackboard,
    ) -> Result<(Action, String)> {
        let mut bound: Option<std::result::Result<BTreeMap<String, bool>, ()>> = None;
        let mut candidates: Vec<(Action, String)> = Vec::new();
        let mut owners: Vec<Arc<Compiled>> = vec![m.clone()];
        match self.methodologies.list(ctx).await {
            Err(err) => {
                tracing::warn!(err = %err, "specializations of other methodologies unavailable")
            }
            Ok(others) => owners.extend(others.into_iter().filter(|o| o.name() != m.name())),
        }
        for owner in &owners {
            for s in owner.specializations_of(m.name(), action.declared()) {
                if !owner.applicable(&s, bb) {
                    continue;
                }
                if !s.required_mcps().is_empty() {
                    if bound.is_none() {
                        bound = Some(self.bound_mcps(ctx, p).await.map_err(|_| ()));
                    }
                    match &bound {
                        Some(Ok(b)) if all_bound(&s, b) => {}
                        _ => continue,
                    }
                }
                let name = if owner.name() != m.name() {
                    format!("{}/{}", owner.name(), s.name)
                } else {
                    s.name.clone()
                };
                candidates.push((s, name));
            }
        }
        let ranked = goap_methodology::rank_by_priority(candidates, |_| true, |c| c.0.priority);
        let Some((best, name)) = ranked.into_iter().next() else {
            if action.kind == KIND_ABSTRACT {
                return Err(Error::msg(format!(
                    "no applicable specialization for abstract action {}",
                    action.name
                )));
            }
            return Ok((action.clone(), String::new()));
        };
        let mut imp = best;
        imp.name = action.name.clone();
        imp.pre = action.pre.clone();
        imp.effects = action.effects.clone();
        imp.expects = action.expects.clone();
        imp.cost = action.cost;
        imp.incremental = action.incremental;
        if imp.permission.is_empty() {
            imp.permission = action.permission.clone();
        }
        Ok((imp, name))
    }

    async fn execute_step(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        bb: &Blackboard,
        action: &Action,
        i: usize,
    ) -> Result<()> {
        let r = self.run_action(ctx, p, m, bb, action, i).await?;
        self.settle_action(p, m, action, i, r).await
    }

    /// run_action runs the executor of the action of step i. It writes nothing on the process but the span of its
    /// own step; settle_action then records what it did.
    pub(crate) async fn run_action(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        bb: &Blackboard,
        action: &Action,
        i: usize,
    ) -> Result<Ran> {
        let snapshot = Arc::new(p.clone());
        let (ctx, end, host, sc, res, err) =
            self.run_action_on(ctx, snapshot, m, bb, action, i).await?;
        p.steps[i].span_id = self.span_id(&ctx);
        Ok(Ran {
            ctx,
            end,
            host,
            sc,
            res,
            err,
        })
    }

    /// run_action_on runs the executor of an action on a snapshot of the process (several at once for a scoped
    /// action).
    #[allow(clippy::type_complexity)]
    pub(crate) async fn run_action_on(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: Arc<Process>,
        m: &Arc<Compiled>,
        bb: &Blackboard,
        action: &Action,
        i: usize,
    ) -> Result<(
        Ctx,
        EndAction,
        Arc<Host>,
        Option<StepContext>,
        ActionResult,
        Option<Error>,
    )> {
        let exec: Arc<dyn Executor> = if action.kind == KIND_CALL {
            Arc::new(CallExecutor)
        } else {
            match self.executors.get(&action.kind) {
                Some(x) => x.clone(),
                None => {
                    return Err(Error::msg(format!(
                        "no executor for action kind {:?}",
                        action.kind
                    )));
                }
            }
        };
        tracing::info!(process = %p.id, agent = %p.agent, action = %action.name, plan = ?p.plan,
            subject = %p.steps.get(i).map(|s| s.subject.as_str()).unwrap_or_default(), "executing action");
        let (ctx, end) = self
            .tracer()
            .start_action(ctx, &p, &action.name, &action.kind);
        let agent_mcps = m
            .agent(&p.agent)
            .map(|a| a.mcps.clone())
            .unwrap_or_default();
        let mut host = self.new_host(p.clone(), action, &agent_mcps);
        host.step = i as i64;
        host.base = p
            .steps
            .get(i)
            .map(|s| s.llm_calls.len() as i64)
            .unwrap_or_default();
        let host = Arc::new(host);
        let sc = step_context(m, &p, action);
        let ac = ActionContext {
            process: p.clone(),
            action: action.clone(),
            blackboard: bb.clone(),
            graph: self.graph.clone(),
            host: Some(host.clone()),
            step: sc.clone(),
            form: None,
        };
        let (res, err) = match exec.execute(&ctx, ac).await {
            Ok(r) => (r, None),
            Err(ExecError { partial, error }) => (partial, Some(error)),
        };
        Ok((ctx, end, host, sc, res, err))
    }

    /// settle_action records on the process what the run of the action of step i did.
    pub(crate) async fn settle_action(
        self: &Arc<Self>,
        p: &mut Process,
        m: &Arc<Compiled>,
        action: &Action,
        i: usize,
        r: Ran,
    ) -> Result<()> {
        let Ran {
            ctx,
            end,
            host,
            sc,
            res,
            err,
        } = r;
        {
            let step = &mut p.steps[i];
            host.record(step);
            step.output = res.output.clone();
            step.sandbox = res.sandbox.clone();
            if !res.method.is_empty() {
                step.specialization = res.method.clone();
            }
        }
        for l in &res.logs {
            let mut l = l.clone();
            l.step = i as i64;
            p.steps[i].logs.push(l.clone());
            self.emit_log(&ctx, p, l).await;
        }
        let mut usage = Usage::default();
        for st in &p.steps {
            usage.add(st.usage);
        }
        p.usage = usage;
        let children = host.children();
        if !children.is_empty() {
            p.children.extend(children);
        }
        if err.is_none() {
            self.remember(p, m, &action.name, res.turn.clone());
        }
        if !res.vars_set.is_empty() {
            p.vars.extend(res.vars_set.clone());
        }
        end(&p.steps[i], err.as_ref());
        if let Some(err) = err {
            p.steps[i].error = err.to_string();
            p.steps[i].ended_at = Some(self.clock());
            self.record_failure(p, &action.name);
            return Ok(());
        }
        if res.suspended {
            // the action waits for a sub-agent: it is retried when the child ends, or earlier on a WakeOn signal
            p.steps[i].ended_at = Some(self.clock());
            p.steps[i].output = format!("suspended: waiting for sub-agent {}", res.child);
            p.status = STATUS_WAITING.into();
            p.pending = Some(HumanTask {
                kind: TASK_AGENT.into(),
                action: action.name.clone(),
                description: action.description.clone(),
                step: i as i64,
                child_process_id: res.child.clone(),
                wake_on: res.wake_on.clone(),
                context: sc,
                ..Default::default()
            });
            return Ok(());
        }
        if let Some(t) = res.merge.clone() {
            // committed, not merged: a person settles the conflicts
            p.steps[i].ended_at = Some(self.clock());
            p.status = STATUS_WAITING.into();
            let description = format!(
                "The change is committed on {}; merging it into {} needs your decision on {} node(s) changed on both sides: keep the value of {} or take the change's.",
                t.from,
                t.into,
                t.conflicts.len(),
                t.into
            );
            p.pending = Some(HumanTask {
                kind: TASK_MERGE.into(),
                action: action.name.clone(),
                step: i as i64,
                merge: Some(t),
                context: sc,
                description,
                ..Default::default()
            });
            return Ok(());
        }
        if let Some(t) = res.rebase.clone() {
            // not applied: the rebase left conflicts
            p.steps[i].ended_at = Some(self.clock());
            p.status = STATUS_WAITING.into();
            let description = format!(
                "The change was rebased onto {}; {} impact(s) conflict: settle them (edit or keep your values), review them again, then continue.",
                t.target(),
                t.impacts.len()
            );
            p.pending = Some(HumanTask {
                kind: TASK_REBASE.into(),
                action: action.name.clone(),
                step: i as i64,
                rebase: Some(t),
                context: sc,
                description,
                ..Default::default()
            });
            return Ok(());
        }
        self.forget_children(p, &action.name);
        if res.wait {
            p.status = STATUS_WAITING.into();
            p.pending = Some(HumanTask {
                kind: TASK_INPUT.into(),
                action: action.name.clone(),
                description: action.description.clone(),
                instructions: action.instructions.clone(),
                node_types: action.node_types.clone(),
                form: action.form.clone(),
                step: i as i64,
                context: sc,
                ..Default::default()
            });
            return Ok(());
        }
        let ctx =
            crate::oversight::with_automated(&crate::nodeops::with_verify(&ctx, action), action);
        let execution = p.steps[i].execution.clone();
        let res2 = match self
            .add_items(
                &ctx,
                p,
                res.items.clone(),
                &action.name,
                &execution,
                false,
                &action.name,
            )
            .await
        {
            Ok((ids, mut nodes, points)) => {
                match self
                    .apply_node_ops(&ctx, p, res.nodes.clone(), &action.name, &execution)
                    .await
                {
                    Ok(more) => {
                        nodes.extend(more);
                        Ok((ids, nodes, points))
                    }
                    Err(e) => Err(e),
                }
            }
            Err(e) => Err(e),
        };
        match res2 {
            Err(err) => {
                p.steps[i].error = err.to_string();
                p.steps[i].ended_at = Some(self.clock());
                self.record_failure(p, &action.name);
                Ok(())
            }
            Ok((ids, nodes, points)) => {
                p.steps[i].items = ids;
                p.steps[i].nodes = nodes;
                p.steps[i].decisions = points;
                self.finish_step(&ctx, p, m, i).await
            }
        }
    }

    /// forget_children drops the sub-agent calls of a finished action execution.
    pub(crate) fn forget_children(&self, p: &mut Process, action: &str) {
        let prefix = format!("{action}#");
        p.children.retain(|k, _| !k.starts_with(&prefix));
    }

    /// run_child runs (or resumes) a sub-agent for a host call, in the methodology of the parent.
    pub(crate) async fn run_child(
        self: &Arc<Self>,
        ctx: &Ctx,
        h: &Host,
        key: &str,
        agent_name: &str,
        intent: &str,
    ) -> Result<goap_dsl::AgentResult> {
        let methodology = h.process.methodology.clone();
        self.run_child_step(ctx, h, key, &methodology, agent_name, "", intent, None)
            .await
    }

    /// run_child_step runs (or resumes) a sub-agent on the change of the host's process, in a methodology with the
    /// goal fixed, as a step of a process runs its agent or nested process (ADR 0034). A child at work suspends the
    /// call (Error::Suspended).
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn run_child_step<'a>(
        self: &'a Arc<Self>,
        ctx: &'a Ctx,
        h: &'a Host,
        key: &'a str,
        methodology: &'a str,
        agent_name: &'a str,
        goal: &'a str,
        intent: &'a str,
        step: Option<StepContext>,
    ) -> BoxFuture<'a, Result<goap_dsl::AgentResult>> {
        Box::pin(async move {
            let parent = h.process.clone();
            let child = if let Some(id) = parent.children.get(key) {
                self.store.get(ctx, id).await?
            } else {
                let mut vars = parent.vars.clone();
                if let Some(s) = &step
                    && !s.item_key.is_empty()
                {
                    // a stream of a step with foreach (ADR 0050): the element it is carried out for
                    vars.insert("item".into(), s.item.clone());
                }
                let c = self
                    .start(
                        ctx,
                        StartRequest {
                            methodology: methodology.to_string(),
                            change_id: parent.change_id.clone(),
                            baseline_id: parent.baseline_id.clone(),
                            agent: agent_name.to_string(),
                            goal: goal.to_string(),
                            intent: intent.to_string(),
                            parent_id: parent.id.clone(),
                            call: key.to_string(),
                            vars,
                            step,
                            ..Default::default()
                        },
                    )
                    .await?;
                h.add_child(key, &c.id);
                if c.status == STATUS_RUNNING {
                    self.run(ctx, &c.id).await?
                } else {
                    c
                }
            };
            let res = goap_dsl::AgentResult {
                status: child.status.to_string(),
                goal: child.goal.clone(),
                process_id: child.id.clone(),
            };
            if child.status == STATUS_COMPLETED
                || child.status == STATUS_FAILED
                || child.status == STATUS_CANCELLED
            {
                return Ok(res); // a stuck child is not an end: the step waits until a person unblocks it
            }
            h.set_waiting_on(&child.id);
            Err(Error::Suspended)
        })
    }

    /// resume_parent wakes a parent process whose action waits for child.
    /// (A boxed future: the save it ends with spawns it again, a cycle the Send check cannot follow otherwise.)
    pub(crate) fn resume_parent(
        self: Arc<Self>,
        parent_id: String,
        child_id: String,
    ) -> BoxFuture<'static, ()> {
        Box::pin(async move { self.resume_parent_inner(&parent_id, &child_id).await })
    }

    async fn resume_parent_inner(self: &Arc<Self>, parent_id: &str, child_id: &str) {
        let ctx = Ctx::background();
        let saved = {
            let _u = self.lock(parent_id).await;
            let Ok(mut p) = self.store.get(&ctx, parent_id).await else {
                return;
            };
            let waits = p.status == STATUS_WAITING
                && p.pending
                    .as_ref()
                    .is_some_and(|t| t.kind == TASK_AGENT && t.child_process_id == child_id);
            if !waits {
                return;
            }
            p.pending = None;
            p.status = STATUS_RUNNING.into();
            let cause = obj(json!({"child": child_id}));
            self.queue(&ctx, &mut p, "sub-agent-ended", Some(cause));
            self.save(&ctx, &mut p, "step").await
        };
        if saved.is_ok() {
            self.schedule_run(parent_id);
        }
    }

    /// allowed reports whether who holds a permission "<type>:<action>" on the process.
    pub(crate) async fn allowed(
        &self,
        ctx: &Ctx,
        p: &Process,
        who: &Principal,
        permission: &str,
    ) -> Result<bool> {
        self.scope()
            .allowed(ctx, &self.r#ref(p), who, permission)
            .await
    }

    /// run_roles are the roles allowed to run an action of a process (ADR 0043): its own, else those of the agent.
    pub(crate) fn run_roles(&self, m: &Compiled, p: &Process, a: &Action) -> Vec<String> {
        if !a.roles.is_empty() {
            return a.roles.clone();
        }
        m.agent(&p.agent)
            .map(|ag| ag.roles.clone())
            .unwrap_or_default()
    }

    /// may_run reports whether who holds, on the project of the process, one of the roles allowed to run an agent or
    /// an action.
    pub(crate) async fn may_run(
        &self,
        ctx: &Ctx,
        p: &Process,
        who: &Principal,
        typ: &str,
        name: &str,
        roles: &[String],
    ) -> Result<bool> {
        self.scope()
            .may_run(ctx, &self.r#ref(p), who, typ, name, roles)
            .await
    }

    /// may_run_agent reports whether the caller of ctx holds, on the project, one of the roles the agent declares
    /// (none declared: any member), and those roles. It starts nothing (ADR 0090).
    pub async fn may_run_agent(
        &self,
        ctx: &Ctx,
        methodology: &str,
        agent: &str,
        project: &str,
    ) -> Result<(bool, Vec<String>)> {
        let m = self.methodologies.methodology(ctx, methodology).await?;
        let Some(ag) = m.agent(agent) else {
            return Err(Error::invalid_state(format!(
                "unknown agent {agent:?} in {methodology}"
            )));
        };
        if ag.roles.is_empty() {
            return Ok((true, Vec::new()));
        }
        let p = Process {
            methodology: methodology.to_string(),
            agent: agent.to_string(),
            project: project.to_string(),
            initiator: goap_authz::principal_from(ctx),
            ..Default::default()
        };
        let ok = self
            .may_run(ctx, &p, &p.initiator, "agent", &ag.name, &ag.roles)
            .await?;
        Ok((ok, ag.roles.clone()))
    }

    /// check_agent_roles refuses to start an agent its initiator may not run (ADR 0043).
    pub(crate) async fn check_roles(&self, ctx: &Ctx, p: &Process) -> Result<()> {
        self.check_agent_roles(ctx, p).await
    }

    async fn check_agent_roles(&self, ctx: &Ctx, p: &Process) -> Result<()> {
        let m = self.methodology_of(ctx, p).await?;
        let Some(ag) = m.agent(&p.agent) else {
            return Ok(());
        };
        if ag.roles.is_empty() {
            return Ok(());
        }
        if !self
            .may_run(ctx, p, &p.initiator, "agent", &ag.name, &ag.roles)
            .await?
        {
            return Err(Error::forbidden(format!(
                "{:?} holds none of the roles {} on project {} to run {}",
                p.initiator.subject,
                ag.roles.join(", "),
                self.r#ref(p).project,
                ag.name
            )));
        }
        Ok(())
    }

    /// may_submit checks that who may perform the pending human task of p (ADR 0035 §2, ADR 0043).
    pub(crate) async fn may_submit(
        &self,
        ctx: &Ctx,
        p: &Process,
        m: &Compiled,
        who: &Principal,
    ) -> Result<()> {
        let pending = p.pending.clone().unwrap_or_default();
        if !self
            .step_allowed(ctx, p, who, pending.context.as_ref(), "perform")
            .await?
        {
            let (role, path) = pending
                .context
                .as_ref()
                .map(|c| {
                    (
                        c.roles
                            .as_ref()
                            .map(|r| r.responsible.clone())
                            .unwrap_or_default(),
                        c.path.clone(),
                    )
                })
                .unwrap_or_default();
            return Err(Error::forbidden(format!(
                "{:?} does not hold the role {role} for step {path}",
                who.subject
            )));
        }
        if let Some(a) = m.action(&pending.action) {
            let roles = self.run_roles(m, p, a);
            if !roles.is_empty() && !self.may_run(ctx, p, who, "action", &a.name, &roles).await? {
                return Err(Error::forbidden(format!(
                    "{:?} holds none of the roles {} on the project for {}",
                    who.subject,
                    roles.join(", "),
                    a.name
                )));
            }
        }
        Ok(())
    }

    /// may_approve reports whether approver may decide the pending approval of p.
    pub(crate) async fn may_approve(
        &self,
        ctx: &Ctx,
        p: &Process,
        approver: &Principal,
    ) -> Result<bool> {
        let pending = p.pending.clone().unwrap_or_default();
        let ok = if pending.permission == PERMISSION_RUN_ACTION {
            // an action only some roles may run (ADR 0043): one of them approves it, holding its permission too
            let mut ok = self
                .may_run(ctx, p, approver, "action", &pending.action, &pending.roles)
                .await?;
            if ok {
                let m = self.methodology_of(ctx, p).await?;
                if let Some(a) = m.action(&pending.action)
                    && !a.permission.is_empty()
                {
                    ok = self.allowed(ctx, p, approver, &a.permission).await?;
                }
            }
            ok
        } else {
            self.allowed(ctx, p, approver, &pending.permission).await?
        };
        if !ok
            && let Some(c) = &pending.context
            && c.roles.as_ref().is_some_and(|r| !r.accountable.is_empty())
        {
            // the accountable role of the step answers for it: it may approve its gates (ADR 0035 §2)
            return self
                .step_allowed(ctx, p, approver, Some(c), "approve")
                .await;
        }
        Ok(ok)
    }

    /// approve decides a pending approval with the permissions of the principal of ctx. On approval the action runs
    /// immediately (call run afterwards to continue); on rejection the action is disabled for this process.
    pub async fn approve(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        approve: bool,
        comment: &str,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_WAITING || p.pending.as_ref().is_none_or(|t| t.kind != TASK_APPROVAL)
        {
            return Err(Error::invalid_state(format!(
                "process {id} has no pending approval"
            )));
        }
        let approver = goap_authz::principal_from(ctx);
        if !self.may_approve(ctx, &p, &approver).await? {
            return Err(Error::forbidden(format!(
                "{:?} lacks {}",
                approver.subject,
                p.pending
                    .as_ref()
                    .map(|t| t.permission.clone())
                    .unwrap_or_default()
            )));
        }
        let m = self.methodology_of(ctx, &p).await?;
        let pending = p.pending.take().unwrap_or_default();
        let i = pending.step as usize;
        let action = m.action(&pending.action).cloned().unwrap_or_default();
        let permission = pending.permission.clone();
        p.status = STATUS_RUNNING.into();
        if i < p.steps.len() {
            p.steps[i].approved_by = approver.subject.clone();
        }
        let cause = obj(json!({"step": i, "action": action.name}));
        self.queue(
            ctx,
            &mut p,
            if approve { "approved" } else { "rejected" },
            Some(cause),
        );
        let rec = Record {
            kind: goap_journal::KIND_APPROVAL.into(),
            step: i as i64,
            action: action.name.clone(),
            actor: approver.subject.clone(),
            data: obj(json!({"approved": approve, "comment": comment, "permission": permission})),
            ..Default::default()
        };
        self.journal(ctx, &mut p, vec![rec]).await;
        if !approve {
            if i < p.steps.len() {
                p.steps[i].error = format!("rejected by {}: {comment}", approver.subject);
                p.steps[i].ended_at = Some(self.clock());
            }
            p.disabled.insert(action.name.clone(), true);
            self.save(ctx, &mut p, "step").await?;
            return Ok(p);
        }
        let bb = self.observe(ctx, &mut p, &m).await?;
        if let Err(err) = self.execute(ctx, &mut p, &m, &bb, &action, i).await {
            self.fail(&mut p, &err);
        }
        let ev = event_of(&p);
        self.save(ctx, &mut p, &ev).await?;
        Ok(p)
    }

    /// finish_step re-observes the blackboard and checks the promised effects. The run of a step of a process that
    /// ended without error is recorded first (journal step.ended), so the history the observation reads has it.
    pub(crate) async fn finish_step(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        i: usize,
    ) -> Result<()> {
        let step = p.steps[i].clone();
        if let Some(a) = m.action(&step.action)
            && !a.step.is_empty()
            && step.error.is_empty()
        {
            let rec = Record {
                kind: goap_journal::KIND_STEP_ENDED.into(),
                step: step.index,
                action: step.action.clone(),
                activity_ref: a.step.clone(),
                subject: step.subject.clone(),
                subject_ref: step.subject_ref.clone(),
                started_at: Some(step.started_at),
                ended_at: Some(self.clock()),
                data: obj(json!({"execution": step.execution, "children": step.children})),
                ..Default::default()
            };
            self.journal(ctx, p, vec![rec]).await;
        }
        let bb = self.observe(ctx, p, m).await?;
        let action = m.action(&step.action).cloned().unwrap_or_default();
        let now = self.clock();
        {
            let s = &mut p.steps[i];
            s.board_after = bb.change.items.len() as i64;
            s.after = p.world.clone();
            s.ended_at = Some(now);
        }
        let mut met = p.world.satisfies(&action.effects);
        if action.is_scoped() {
            // a run for one subject: its effects are those of the subject, the aggregates come once every subject is
            // done
            met = crate::scoped::subject_holds(p, &action, &step.subject)
                && p.world.satisfies(&crate::scoped::change_effects(&action));
        }
        p.steps[i].effects_met = met;
        if !met {
            let s = &p.steps[i];
            if action.incremental && s.items.len() + s.nodes.len() + s.decisions.len() > 0 {
                p.steps[i].progress = true; // the action runs again on the next cycle
                return Ok(());
            }
            self.record_failure(p, &action.name);
            return Ok(());
        }
        self.emit_events(ctx, p, &action, i).await?;
        if !action.step.is_empty() && p.world.satisfies(&action.effects) {
            let st = p.steps[i].clone();
            self.step_completed(ctx, p, m, &action, &st).await;
        }
        Ok(())
    }

    /// emit_events raises the events of the methodology an action declares in emits once it reached its effects
    /// (ADR 0110): a signal item of each name on the change.
    async fn emit_events(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        action: &Action,
        i: usize,
    ) -> Result<()> {
        if action.emits.is_empty() || p.change_id.is_empty() {
            return Ok(());
        }
        let mut items = Vec::with_capacity(action.emits.len());
        for name in &action.emits {
            let mut data = Map::new();
            let act = if action.implements.is_empty() {
                &action.name
            } else {
                &action.implements
            };
            data.insert("action".into(), Value::String(act.clone()));
            if !action.step.is_empty() {
                data.insert("step".into(), Value::String(action.step.clone()));
            }
            items.push(ChangeItem {
                id: ItemID::from(uuid::Uuid::new_v4().to_string()),
                r#type: goap_domain::TYPE_SIGNAL,
                target: name.clone(),
                status: goap_domain::ITEM_PROPOSED,
                data,
                produced_by: action.name.clone(),
                execution: p.steps[i].execution.clone(),
                flow: p.flow.clone(),
                ..Default::default()
            });
        }
        let added = self
            .graph
            .add_items(ctx, &p.change_id, items)
            .await
            .map_err(|e| Error::from(e).context(format!("emit {}", action.emits.join(", "))))?;
        p.steps[i].items.extend(item_ids(&added));
        self.wake_on_signals(ctx, p, &added).await;
        Ok(())
    }

    pub(crate) fn record_failure(&self, p: &mut Process, action: &str) {
        let max = self.max_failures_or_default();
        let mut n = 0;
        for s in &p.steps {
            if s.action == action && !s.effects_met && !s.progress && s.ended_at.is_some() {
                if !s.subject.is_empty() {
                    // a scoped action fails subject by subject
                    return;
                }
                n += 1;
            }
        }
        if n >= max {
            p.disabled.insert(action.to_string(), true);
            tracing::warn!(process = %p.id, action, failures = n, "action disabled");
        }
    }

    /// observe reads the blackboard of p and evaluates the methodology's conditions against it. A process not yet
    /// bound to a change observes only its own vars.
    pub(crate) async fn observe(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
    ) -> Result<Blackboard> {
        let mut bb = if p.change_id.is_empty() {
            Blackboard {
                vars: p.vars.clone(),
                ..Default::default()
            }
        } else {
            let mut bb = self.blackboard(ctx, &p.change_id, &p.flow).await?;
            bb.vars = p.vars.clone();
            p.org = bb.change.owner_org.clone();
            p.project = bb.change.project_id.clone();
            bb
        };
        bb.supertypes = self.supertypes_of(m).into_iter().collect();
        let res = m.conditions.evaluate(&bb);
        p.world = WorldState(res.state);
        p.unknown = res.errors;
        p.subjects = res.subjects;
        Ok(bb)
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn add_items(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        input: Vec<ItemInput>,
        produced_by: &str,
        execution: &str,
        human: bool,
        by: &str,
    ) -> Result<(Vec<ItemID>, Vec<ChangeImpactID>, Vec<String>)> {
        // change impact operations are applied in order, after the items; the decision operations last (ADR 0009 §4)
        let mut ops = Vec::new();
        let mut decisions: Vec<DecisionOp> = Vec::new();
        let mut items = Vec::with_capacity(input.len());
        for it in input {
            if it.r#type == "decisionPoint" {
                let Some(d) = it.decision_point else {
                    return Err(Error::invalid_state(
                        "item of kind decisionPoint needs a decisionPoint operation",
                    ));
                };
                decisions.push(d);
                continue;
            }
            if it.r#type == "changeImpact" {
                let Some(op) = it.change_impact else {
                    return Err(Error::invalid_state(
                        "item of kind changeImpact needs a changeImpact operation",
                    ));
                };
                ops.push(op);
                continue;
            }
            items.push(it);
        }
        let ids = self
            .add_plain_items(ctx, p, items, produced_by, execution)
            .await?;
        let nodes = self
            .apply_node_ops(ctx, p, ops, produced_by, execution)
            .await?;
        let points = self
            .apply_decision_ops(ctx, p, decisions, human, by)
            .await?;
        Ok((ids, nodes, points))
    }

    async fn add_plain_items(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        input: Vec<ItemInput>,
        produced_by: &str,
        execution: &str,
    ) -> Result<Vec<ItemID>> {
        if input.is_empty() {
            return Ok(Vec::new());
        }
        if p.change_id.is_empty() {
            return Err(Error::msg(format!(
                "process {} has no change attached: call goap-scheduler/attach first (ADR 0031)",
                p.id
            )));
        }
        let bb = self.graph.blackboard_in(ctx, &p.change_id, &p.flow).await?;
        let mut items = Resolver::new(&bb.change, || uuid::Uuid::new_v4().to_string())
            .resolve(&input, produced_by)?;
        for it in &mut items {
            it.execution = execution.to_string();
            it.flow = p.flow.clone();
        }
        let added = self.graph.add_items(ctx, &p.change_id, items).await?;
        let ids = item_ids(&added);
        self.wake_on_signals(ctx, p, &added).await;
        Ok(ids)
    }

    /// wake_on_signals wakes a live parent process suspended on this process as soon as one of the newly written items
    /// is a signal, addressed to it or broadcast, whose name the parent's action declared via WakeOn.
    pub(crate) async fn wake_on_signals(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &Process,
        items: &[ChangeItem],
    ) {
        if p.parent_id.is_empty() {
            return;
        }
        let names: Vec<&str> = items
            .iter()
            .filter(|it| {
                it.r#type == goap_domain::TYPE_SIGNAL
                    && (it.recipient.is_empty() || it.recipient == p.parent_id)
            })
            .map(|it| it.target.as_str())
            .collect();
        if names.is_empty() {
            return;
        }
        let Ok(parent) = self.store.get(ctx, &p.parent_id).await else {
            return;
        };
        let Some(t) = parent.pending.as_ref() else {
            return;
        };
        if parent.status != STATUS_WAITING || t.kind != TASK_AGENT || t.child_process_id != p.id {
            return;
        }
        if names.iter().any(|n| t.wake_on.iter().any(|w| w == n)) {
            let (e, parent_id, child_id) = (self.clone(), p.parent_id.clone(), p.id.clone());
            self.background(e.resume_parent(parent_id, child_id));
        }
    }

    pub(crate) async fn emit_log(&self, ctx: &Ctx, p: &Process, mut l: LogLine) {
        let Some(events) = &self.events else {
            return;
        };
        if l.process_id.is_empty() {
            l.process_id = p.id.clone();
        }
        let time = l.time;
        let ev = ProcessEvent {
            event: "log".into(),
            process: None,
            step: None,
            log: Some(l),
            time,
        };
        let _ = events
            .publish(
                ctx,
                &format!("goap.process.{}.log", p.id),
                &Payload::new(&ev),
            )
            .await;
    }

    pub(crate) async fn save(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        event: &str,
    ) -> Result<()> {
        p.updated_at = self.clock();
        if p.status.terminal() {
            p.children = BTreeMap::new();
        }
        if p.trace_id.is_empty() {
            p.trace_id = self.tracer().trace_id(ctx);
        }
        if p.status.terminal() && event == p.status.as_str() {
            let mut r = end_record(p);
            r.ended_at = Some(p.updated_at);
            self.journal(ctx, p, vec![r]).await;
        }
        self.store.put(ctx, p).await?;
        if (p.status.terminal() || p.status == STATUS_STUCK)
            && let Some(s) = &self.sandboxes
        {
            s.release(ctx, &p.id).await;
        }
        if p.status.terminal() && !p.parent_id.is_empty() {
            let (e, parent, child) = (self.clone(), p.parent_id.clone(), p.id.clone());
            self.background(e.resume_parent(parent, child));
        }
        if let Some(events) = &self.events {
            let ev = ProcessEvent {
                event: event.to_string(),
                process: Some(p.clone()),
                step: None,
                log: None,
                time: p.updated_at,
            };
            if let Err(err) = events
                .publish(
                    ctx,
                    &format!("goap.process.{}.{event}", p.id),
                    &Payload::new(&ev),
                )
                .await
            {
                tracing::warn!(err = %err, "publish failed");
            }
        }
        Ok(())
    }

    /// supertypes_of is the ancestry of the node types for the conditions (`x.types`): the catalogue in force, else
    /// the types the methodology was resolved with.
    pub(crate) fn supertypes_of(&self, m: &Compiled) -> BTreeMap<String, Vec<String>> {
        if let Some(t) = &self.types
            && let Some(t) = t()
        {
            return t.supertypes().into_iter().collect();
        }
        m.supertypes()
    }
}

/// obj turns a json! object into its map.
pub(crate) fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}
