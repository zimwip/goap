//! What the assistant asks of the engine, always as the caller (ADR 0090, `engine.go`).

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::principal_from;
use goap_ctx::Ctx;
use goap_gen::engine::v1 as enginev1;
use goap_gen::registry::v1 as registryv1;
use serde_json::{Map, Value};

use crate::service::BoxError;

/// Engine is what the assistant asks of the engine, always as the caller (ADR 0090): which agents they may run on a
/// project, to start a process and the processes running (EngineClient).
#[async_trait]
pub trait Engine: Send + Sync {
    /// CheckAgents answers whether the caller may start a process of the methodology on the project and which of the
    /// agents (all when none is named) they may run there.
    async fn check_agents(
        &self,
        ctx: &Ctx,
        methodology: &str,
        project: &str,
        agents: &[String],
    ) -> Result<AgentChecks, BoxError>;
    async fn start_process(&self, ctx: &Ctx, in_: StartProcess) -> Result<ProcessInfo, BoxError>;
    /// StartingPoints lists the steps of the methodology of the change that are possible now towards its goal, as the
    /// caller (ADR 0097): the only steps the assistant may propose.
    async fn starting_points(&self, ctx: &Ctx, change_id: &str) -> Result<Points, BoxError>;
    /// Active lists the processes not finished (clarifying, running, waiting, stuck) the caller may read.
    async fn active(&self, ctx: &Ctx) -> Result<Vec<ProcessInfo>, BoxError>;
}

/// AgentChecks is the answer of Engine.check_agents: the agents the caller may run, by name.
#[derive(Debug, Clone, Default)]
pub struct AgentChecks {
    pub may_start: bool,
    pub may_run: HashMap<String, bool>,
}

/// Points is the answer of Engine.starting_points.
#[derive(Debug, Clone, Default)]
pub struct Points {
    pub goal: String,
    pub reason: String,
    pub points: Vec<Point>,
    /// The steps towards the goal that wait for conditions; they are not proposable.
    pub blocked_count: i64,
    /// "<step>: <missing conditions>", capped.
    pub blocked: Vec<String>,
}

/// Point is a step (or step of a method) that is possible now. Agent and AgentGoal are what starts it.
#[derive(Debug, Clone, Default)]
pub struct Point {
    pub id: String,
    pub kind: String,
    pub name: String,
    pub description: String,
    pub method: String,
    pub process: String,
    pub why: Vec<String>,
    pub produces: Vec<String>,
    pub responsible: String,
    pub agent: String,
    pub agent_goal: String,
    pub may_run: bool,
    pub running: bool,
}

impl Point {
    /// Whether the caller may start the point now.
    pub fn possible(&self) -> bool {
        self.may_run && !self.running
    }
}

impl Points {
    /// The point of an id.
    pub fn find(&self, id: &str) -> Option<Point> {
        self.points.iter().find(|p| p.id == id).cloned()
    }
}

/// StartProcess is the start of an agent: its goal, or the intent the engine identifies the goal from.
#[derive(Debug, Clone, Default)]
pub struct StartProcess {
    pub methodology: String,
    pub agent: String,
    pub goal: String,
    pub intent: String,
    pub title: String,
    pub change_id: String,
    pub project_id: String,
    pub vars: Map<String, Value>,
}

/// ProcessInfo is what the assistant keeps of a process.
#[derive(Debug, Clone, Default)]
pub struct ProcessInfo {
    pub id: String,
    pub methodology: String,
    pub agent: String,
    pub change_id: String,
    pub status: String,
}

/// EngineApi is the part of the engine service the assistant calls: the engine handler (one process) and its Connect
/// client (distributed platform) both implement it.
pub type EngineApi = dyn enginev1::EngineService;

/// ViewApi is the part of the methodology registry the assistant reads the compiled view of a methodology version
/// through (ADR 0126 step 5).
pub type ViewApi = dyn registryv1::RegistryService;

/// EngineClient is the Engine over the engine service, with the identity of the caller of the context on every
/// request: the engine authorizes them. Views gives what the steps the engine binds are (None: the points keep their
/// ids, launches and bindings only).
#[derive(Clone)]
pub struct EngineClient {
    pub api: Arc<EngineApi>,
    pub views: Option<Arc<ViewApi>>,
}

fn as_caller<T>(ctx: &Ctx, msg: T) -> goap_connect::Request<T> {
    let mut r = goap_connect::Request::new(msg);
    goap_identity::set_headers(&principal_from(ctx), &mut r.headers);
    r
}

fn info_of(p: &enginev1::Process) -> ProcessInfo {
    ProcessInfo {
        id: p.id.clone(),
        methodology: p.methodology.clone(),
        agent: p.agent.clone(),
        change_id: p.change_id.clone(),
        status: p.status.clone(),
    }
}

#[async_trait]
impl Engine for EngineClient {
    async fn check_agents(
        &self,
        ctx: &Ctx,
        methodology: &str,
        project: &str,
        agents: &[String],
    ) -> Result<AgentChecks, BoxError> {
        let r = self
            .api
            .check_agents(
                ctx,
                as_caller(
                    ctx,
                    enginev1::CheckAgentsRequest {
                        methodology: methodology.into(),
                        project_id: project.into(),
                        agents: agents.to_vec(),
                    },
                ),
            )
            .await?;
        let mut out = AgentChecks {
            may_start: r.msg.may_start,
            may_run: HashMap::new(),
        };
        for a in &r.msg.agents {
            out.may_run.insert(a.agent.clone(), a.may_run);
        }
        Ok(out)
    }

    async fn start_process(&self, ctx: &Ctx, in_: StartProcess) -> Result<ProcessInfo, BoxError> {
        let r = self
            .api
            .start_process(
                ctx,
                as_caller(
                    ctx,
                    enginev1::StartProcessRequest {
                        methodology: in_.methodology,
                        agent: in_.agent,
                        goal: in_.goal,
                        intent: in_.intent,
                        title: in_.title,
                        change_id: in_.change_id,
                        project_id: in_.project_id,
                        vars: goap_pbconv::to_struct(&in_.vars),
                        ..Default::default()
                    },
                ),
            )
            .await?;
        match &r.msg.process {
            Some(p) => Ok(info_of(p)),
            None => Err("the engine started no process".into()),
        }
    }

    async fn active(&self, ctx: &Ctx) -> Result<Vec<ProcessInfo>, BoxError> {
        let r = self
            .api
            .list_processes(
                ctx,
                as_caller(
                    ctx,
                    enginev1::ListProcessesRequest {
                        statuses: ["clarifying", "running", "waiting", "stuck"]
                            .map(String::from)
                            .to_vec(),
                        ..Default::default()
                    },
                ),
            )
            .await?;
        Ok(r.msg.processes.iter().map(info_of).collect())
    }

    async fn starting_points(&self, ctx: &Ctx, change_id: &str) -> Result<Points, BoxError> {
        let r = self
            .api
            .list_starting_points(
                ctx,
                as_caller(
                    ctx,
                    enginev1::ListStartingPointsRequest {
                        change_id: change_id.into(),
                        ..Default::default()
                    },
                ),
            )
            .await?;
        let b = &r.msg;
        // the engine binds the steps to the change, the view of the methodology version says what they are
        let mut view = None;
        if let Some(views) = &self.views
            && b.points.len() + b.blocked.len() > 0
        {
            let v = views
                .get_methodology_view(
                    ctx,
                    as_caller(
                        ctx,
                        registryv1::GetMethodologyViewRequest {
                            name: b.methodology.clone(),
                            version: b.version.clone(),
                            ..Default::default()
                        },
                    ),
                )
                .await
                .map_err(|e| {
                    format!(
                        "the view of methodology {} {} cannot be read: {e}",
                        b.methodology, b.version
                    )
                })?;
            view = v.msg.view;
        }
        let sp = goap_startingpoint::compose(view.as_ref(), b);
        let mut out = Points {
            goal: sp.goal,
            reason: sp.reason,
            blocked_count: sp.blocked_count,
            ..Default::default()
        };
        for p in sp.points {
            out.points.push(Point {
                id: p.id,
                kind: p.kind,
                name: p.name,
                description: p.description,
                method: p.method,
                process: p.process,
                why: p.why,
                produces: p.produces,
                responsible: p.responsible,
                agent: p.launch.agent,
                agent_goal: p.launch.goal,
                may_run: p.may_run,
                running: p.running,
            });
        }
        for bl in sp.blocked {
            out.blocked
                .push(format!("{}: {}", bl.id, bl.missing.join(", ")));
        }
        Ok(out)
    }
}
