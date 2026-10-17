//! The Connect engine service: the process engine over the network (Go's `handler.go`).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use goap_authz::{self as authz, Authorizer, Principal, Request as AuthzRequest, Resource};
use goap_connect::{ConnectError, Ctx, Request, Response, ServerStream};
use goap_domain::{BaselineID, ChangeID, NodeID};
use goap_engine::{
    self as engine, AttachRequest, Broker, Engine, ItemInput, Process, StartRequest,
    StartingPointsOptions, TriggerManager, UnblockRequest,
};
use goap_gen::engine::v1 as pb;
use goap_graphapi::Resolution;
use goap_identity::Extractor;
use goap_pbconv as pbconv;
use serde_json::Value;

/// The function a [`Handler`] runs a process with in the background.
pub type RunFn = Arc<dyn Fn(String) + Send + Sync>;

/// Handler implements the engine service (`EngineService`). Processes run in background tasks; clients poll
/// GetProcess or listen to the events.
#[derive(Clone)]
pub struct Handler {
    pub engine: Arc<Engine>,
    /// Run executes a process in the background (defaults to a tokio task).
    pub run: Option<RunFn>,
    /// DefaultPrincipal is used when the request carries no identity headers (single-process dev without gateway).
    /// None keeps such callers anonymous.
    pub default_principal: Option<Principal>,
    /// Authz authorizes process operations (resource "process"; actions start, submit, read). None grants
    /// everything.
    pub authz: Option<Arc<dyn Authorizer>>,
    /// Broker streams live events (WatchEvents).
    pub broker: Option<Arc<Broker>>,
    /// Triggers runs agents automatically (None: triggers disabled).
    pub triggers: Option<Arc<TriggerManager>>,
}

impl Handler {
    /// A handler of the engine with every option unset.
    pub fn new(engine: Arc<Engine>) -> Handler {
        Handler {
            engine,
            run: None,
            default_principal: None,
            authz: None,
            broker: None,
            triggers: None,
        }
    }

    /// authorize checks an operation on a process (p None for start, on the caller's active project): on the
    /// project of the process, where the caller's roles are held (ADR 0043).
    async fn authorize(
        &self,
        ctx: &Ctx,
        action: &str,
        methodology: &str,
        p: Option<&Process>,
    ) -> Result<(), Error> {
        Ok(authorize(self.authz.as_deref(), ctx, action, methodology, p).await?)
    }

    /// load_authorized loads a process and checks the operation on it.
    async fn load_authorized(&self, ctx: &Ctx, id: &str, action: &str) -> Result<(), Error> {
        let p = self.engine.store.get(ctx, id).await?;
        self.authorize(ctx, action, "", Some(&p)).await?;
        Ok(())
    }

    fn principal(&self, ctx: &Ctx, hdr: &goap_connect::axum::http::HeaderMap) -> Ctx {
        Extractor {
            default: self.default_principal.clone(),
        }
        .context(ctx, hdr)
    }

    fn run_process(&self, p: &Process) {
        if p.status != engine::STATUS_RUNNING {
            return;
        }
        if let Some(run) = &self.run {
            run(p.id.clone());
            return;
        }
        let e = self.engine.clone();
        let id = p.id.clone();
        tokio::spawn(async move {
            if let Err(err) = e.run(&Ctx::background(), &id).await {
                tracing::error!(process = %id, err = %err, "run");
            }
        });
    }

    /// The change of a request, read like the change itself: a personal change is its subject's only.
    async fn readable_change(
        &self,
        ctx: &Ctx,
        id: &str,
    ) -> Result<goap_domain::Change, ConnectError> {
        let ch = self
            .engine
            .graph
            .change(ctx, &ChangeID::from(id))
            .await
            .map_err(to_connect)?;
        if goap_access::is_personal(&ch)
            && !goap_access::is_personal_to(&ch, &authz::principal_from(ctx).subject)
        {
            return Err(ConnectError::not_found(format!("change {id} not found")));
        }
        Ok(ch)
    }
}

async fn authorize(
    a: Option<&dyn Authorizer>,
    ctx: &Ctx,
    action: &str,
    methodology: &str,
    p: Option<&Process>,
) -> Result<(), authz::Error> {
    let who = authz::principal_from(ctx);
    let res = match p {
        None => Resource {
            r#type: "process".into(),
            name: methodology.into(),
            org: who.org.clone(),
            owner: who.subject.clone(),
            project_id: who.project.clone(),
            ..Default::default()
        },
        Some(p) => Resource {
            r#type: "process".into(),
            id: p.id.clone(),
            name: p.methodology.clone(),
            org: p.initiator.org.clone(),
            owner: p.initiator.subject.clone(),
            project_id: p.project.clone(),
            ..Default::default()
        },
    };
    authz::check(
        ctx,
        a,
        &AuthzRequest {
            subject: who,
            action: action.into(),
            resource: res,
        },
    )
    .await
}

/// The errors a handler meets: the engine's and the authorizer's.
#[derive(Debug, thiserror::Error)]
enum Error {
    #[error(transparent)]
    Engine(#[from] engine::Error),
    #[error(transparent)]
    Authz(#[from] authz::Error),
}

impl From<Error> for ConnectError {
    fn from(e: Error) -> Self {
        match e {
            Error::Engine(e) => to_connect(e),
            Error::Authz(e) => to_connect(e),
        }
    }
}

/// The Connect error of an error of the engine, the authorizer or the graph (Go's `toConnect`): an unknown process
/// or methodology is `not_found`, a refusal `permission_denied`, an invalid process state `failed_precondition`,
/// the rest as `rpcerr::to_connect` says.
pub fn to_connect<E>(err: E) -> ConnectError
where
    E: Into<Box<dyn std::error::Error + Send + Sync + 'static>>,
{
    let err: Box<dyn std::error::Error + Send + Sync + 'static> = err.into();
    if let Some(e) = err.downcast_ref::<engine::Error>() {
        let msg = e.to_string();
        if e.is_not_found() || e.is_unknown_methodology() {
            return ConnectError::not_found(msg);
        }
        if e.is_forbidden() {
            return ConnectError::permission_denied(msg);
        }
        if e.is_invalid_state() {
            return ConnectError::failed_precondition(msg);
        }
        if let Some(g) = e.graph() {
            let mut ce = goap_rpcerr::to_connect(g.clone());
            if ce.message() != msg {
                // the graph's code, the engine's whole message
                let meta = ce.meta().clone();
                ce = ConnectError::new(ce.code(), msg);
                *ce.meta_mut() = meta;
            }
            return ce;
        }
        if e.is_canceled() {
            return ConnectError::canceled(msg);
        }
        if e.is_deadline_exceeded() {
            return ConnectError::deadline_exceeded(msg);
        }
    }
    if let Some(e) = err.downcast_ref::<authz::Error>()
        && e.is_forbidden()
    {
        return ConnectError::permission_denied(e.to_string());
    }
    goap_rpcerr::to_connect(err)
}

fn s(id: &impl std::ops::Deref<Target = str>) -> String {
    id.to_string()
}

#[goap_connect::async_trait]
impl pb::EngineService for Handler {
    async fn start_process(
        &self,
        ctx: &Ctx,
        r: Request<pb::StartProcessRequest>,
    ) -> Result<Response<pb::StartProcessResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let mut project_id = m.project_id.clone();
        if project_id.is_empty() && !m.change_id.is_empty() {
            // a run on an existing change works where the change does (ADR 0091), not where the caller's token points
            if let Ok(c) = self
                .engine
                .graph
                .change(&ctx, &ChangeID::from(m.change_id.as_str()))
                .await
            {
                project_id = c.project_id;
            }
        }
        if project_id.is_empty() {
            project_id = authz::principal_from(&ctx).project;
        }
        // starting is checked on the project the process will work on, where the caller's roles are held
        let mut who = authz::principal_from(&ctx);
        who.project = project_id.clone();
        self.authorize(
            &authz::with_principal(&ctx, who),
            "start",
            &m.methodology,
            None,
        )
        .await?;
        let p = self
            .engine
            .start(
                &ctx,
                StartRequest {
                    methodology: m.methodology,
                    change_id: ChangeID::from(m.change_id),
                    baseline_id: BaselineID::from(m.baseline_id),
                    title: m.title,
                    intent: m.intent,
                    goal: m.goal,
                    agent: m.agent,
                    owner_org: m.owner_org,
                    project_id,
                    vars: pbconv::to_map(m.vars.as_ref()),
                    ..Default::default()
                },
            )
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::StartProcessResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    /// CheckAgents answers, without starting anything, whether the caller may start a process of a methodology on a
    /// project and which of its agents they may run there (ADR 0090): the checks of StartProcess and Engine::start.
    async fn check_agents(
        &self,
        ctx: &Ctx,
        r: Request<pb::CheckAgentsRequest>,
    ) -> Result<Response<pb::CheckAgentsResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let mut project_id = m.project_id.clone();
        if project_id.is_empty() {
            project_id = authz::principal_from(&ctx).project;
        }
        let mut who = authz::principal_from(&ctx);
        who.project = project_id.clone();
        let ctx = authz::with_principal(&ctx, who);
        let mut out = pb::CheckAgentsResponse::default();
        if self
            .authorize(&ctx, "start", &m.methodology, None)
            .await
            .is_err()
        {
            return Ok(Response::new(out));
        }
        out.may_start = true;
        let meth = self
            .engine
            .methodologies
            .methodology(&ctx, &m.methodology)
            .await
            .map_err(to_connect)?;
        let mut names = m.agents.clone();
        if names.is_empty() {
            names = meth.agent_list().into_iter().map(|a| a.name).collect();
        }
        for n in names {
            let (ok, roles) = self
                .engine
                .may_run_agent(&ctx, &m.methodology, &n, &project_id)
                .await
                .map_err(to_connect)?;
            out.agents.push(pb::AgentCheck {
                agent: n,
                may_run: ok,
                roles,
            });
        }
        Ok(Response::new(out))
    }

    /// ListStartingPoints serves the steps of the methodology of a change that are possible now towards its goal
    /// (ADR 0097): the checks of StartProcess (the "start" permission on the project of the change, the roles of the
    /// agent that would run each point, reported per point as may_run) and the read of the change (a personal change
    /// is its subject's only).
    async fn list_starting_points(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListStartingPointsRequest>,
    ) -> Result<Response<pb::ListStartingPointsResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let ch = self.readable_change(&ctx, &m.change_id).await?;
        let mut project_id = m.project_id.clone();
        if project_id.is_empty() {
            project_id = ch.project_id.clone();
        }
        if project_id.is_empty() {
            project_id = authz::principal_from(&ctx).project;
        }
        let mut who = authz::principal_from(&ctx);
        who.project = project_id.clone();
        let ctx = authz::with_principal(&ctx, who);
        let mut name = m.methodology.clone();
        if name.is_empty() {
            name = ch.methodology.clone();
        }
        if !name.is_empty() {
            self.authorize(&ctx, "start", &name, None).await?;
        }
        let sp = self
            .engine
            .starting_points(
                &ctx,
                &ch.id,
                StartingPointsOptions {
                    methodology: m.methodology,
                    project: project_id,
                    ..Default::default()
                },
            )
            .await
            .map_err(to_connect)?;
        Ok(Response::new(starting_points_to_pb(&sp)))
    }

    async fn attach_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::AttachChangeRequest>,
    ) -> Result<Response<pb::AttachChangeResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "attach").await?;
        let mut project_id = m.project_id.clone();
        if project_id.is_empty() {
            project_id = authz::principal_from(&ctx).project;
        }
        self.engine
            .attach_change(
                &ctx,
                &m.process_id,
                AttachRequest {
                    change_id: ChangeID::from(m.change_id),
                    title: m.title,
                    intent: m.intent,
                    namespace: m.namespace,
                    owner_org: m.owner_org,
                    project_id,
                    baseline_id: BaselineID::from(m.baseline_id),
                },
            )
            .await
            .map_err(to_connect)?;
        let p = self
            .engine
            .store
            .get(&ctx, &m.process_id)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::AttachChangeResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn get_process_log(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetProcessLogRequest>,
    ) -> Result<Response<pb::GetProcessLogResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let id = r.msg.process_id;
        self.load_authorized(&ctx, &id, "read").await?;
        let entries = self
            .engine
            .store
            .list_process_log(&ctx, &id)
            .await
            .map_err(to_connect)?;
        let mut out = pb::GetProcessLogResponse::default();
        for e in entries {
            // Go marshals the payload map: a nil map is "null"
            let payload = if e.payload.is_empty() {
                "null".to_string()
            } else {
                go_json(&Value::Object(e.payload.clone()))
            };
            out.entries.push(pb::ProcessLogEntry {
                seq: e.seq,
                process_id: e.process_id.clone(),
                r#type: e.r#type.clone(),
                payload,
                at: pbconv::time(&e.at),
            });
        }
        Ok(Response::new(out))
    }

    async fn answer_intent(
        &self,
        ctx: &Ctx,
        r: Request<pb::AnswerIntentRequest>,
    ) -> Result<Response<pb::AnswerIntentResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "submit").await?;
        let p = self
            .engine
            .answer(&ctx, &m.process_id, &m.answer)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::AnswerIntentResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn submit_human_input(
        &self,
        ctx: &Ctx,
        r: Request<pb::SubmitHumanInputRequest>,
    ) -> Result<Response<pb::SubmitHumanInputResponse>, ConnectError> {
        let mut items = Vec::with_capacity(r.msg.items.len());
        for st in &r.msg.items {
            let v = Value::Object(pbconv::struct_to_map(st));
            let it: ItemInput = serde_json::from_value(v)
                .map_err(|e| ConnectError::invalid_argument(e.to_string()))?;
            items.push(it);
        }
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "submit").await?;
        let p = match &m.form {
            Some(form) => {
                if !items.is_empty() {
                    return Err(ConnectError::invalid_argument(
                        "send the values of the form or items, not both",
                    ));
                }
                self.engine
                    .submit_form(&ctx, &m.process_id, pbconv::struct_to_map(form))
                    .await
            }
            None => self.engine.submit(&ctx, &m.process_id, items).await,
        }
        .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::SubmitHumanInputResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn approve_action(
        &self,
        ctx: &Ctx,
        r: Request<pb::ApproveActionRequest>,
    ) -> Result<Response<pb::ApproveActionResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let p = self
            .engine
            .approve(&ctx, &m.process_id, m.approve, &m.comment)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::ApproveActionResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn unblock_process(
        &self,
        ctx: &Ctx,
        r: Request<pb::UnblockProcessRequest>,
    ) -> Result<Response<pb::UnblockProcessResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let p = self
            .engine
            .unblock(
                &ctx,
                &m.process_id,
                UnblockRequest {
                    decision: m.decision,
                    reason: m.reason,
                },
            )
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::UnblockProcessResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn cancel_process(
        &self,
        ctx: &Ctx,
        r: Request<pb::CancelProcessRequest>,
    ) -> Result<Response<pb::CancelProcessResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let p = self
            .engine
            .cancel(&ctx, &m.process_id, &m.reason)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::CancelProcessResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn relaunch_step(
        &self,
        ctx: &Ctx,
        r: Request<pb::RelaunchStepRequest>,
    ) -> Result<Response<pb::RelaunchStepResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "relaunch")
            .await?;
        // Go's int(step): a negative step reaches the engine as such and is refused there; usize wraps it to a value
        // no process has
        let step = usize::try_from(m.step).unwrap_or(usize::MAX);
        let p = self
            .engine
            .relaunch(&ctx, &m.process_id, step, &m.reason, &m.guidance)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::RelaunchStepResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn decide_flow(
        &self,
        ctx: &Ctx,
        r: Request<pb::DecideFlowRequest>,
    ) -> Result<Response<pb::DecideFlowResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "decide_flow")
            .await?;
        let p = self
            .engine
            .decide_flow(&ctx, &m.process_id, m.adopt, &m.comment)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::DecideFlowResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn resolve_board(
        &self,
        ctx: &Ctx,
        r: Request<pb::ResolveBoardRequest>,
    ) -> Result<Response<pb::ResolveBoardResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "relaunch")
            .await?;
        let (p, np) = self
            .engine
            .resolve_board(&ctx, &m.process_id, m.relaunch, &m.comment)
            .await
            .map_err(to_connect)?;
        let mut out = pb::ResolveBoardResponse {
            process: Some(process_to_pb(&p)),
            ..Default::default()
        };
        if let Some(np) = np {
            out.relaunched = Some(process_to_pb(&np));
            self.run_process(&np);
        }
        self.run_process(&p);
        Ok(Response::new(out))
    }

    async fn resolve_merge(
        &self,
        ctx: &Ctx,
        r: Request<pb::ResolveMergeRequest>,
    ) -> Result<Response<pb::ResolveMergeResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "submit").await?;
        let mut resolutions = BTreeMap::new();
        for (id, rs) in &m.resolutions {
            resolutions.insert(
                NodeID::from(id.as_str()),
                Resolution {
                    props: pbconv::to_map(rs.props.as_ref()),
                    skip: rs.skip,
                },
            );
        }
        let p = self
            .engine
            .resolve_merge(&ctx, &m.process_id, resolutions, &m.comment)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::ResolveMergeResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn resolve_rebase(
        &self,
        ctx: &Ctx,
        r: Request<pb::ResolveRebaseRequest>,
    ) -> Result<Response<pb::ResolveRebaseResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        self.load_authorized(&ctx, &m.process_id, "submit").await?;
        let p = self
            .engine
            .resolve_rebase(&ctx, &m.process_id, &m.comment)
            .await
            .map_err(to_connect)?;
        self.run_process(&p);
        Ok(Response::new(pb::ResolveRebaseResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    async fn get_process(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetProcessRequest>,
    ) -> Result<Response<pb::GetProcessResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let p = self
            .engine
            .store
            .get(&ctx, &r.msg.id)
            .await
            .map_err(to_connect)?;
        self.authorize(&ctx, "read", "", Some(&p)).await?;
        Ok(Response::new(pb::GetProcessResponse {
            process: Some(process_to_pb(&p)),
        }))
    }

    /// GetProcessProgress serves where a run stands in the steps of its process (ADR 0035 §3).
    async fn get_process_progress(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetProcessProgressRequest>,
    ) -> Result<Response<pb::GetProcessProgressResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let p = self
            .engine
            .store
            .get(&ctx, &r.msg.id)
            .await
            .map_err(to_connect)?;
        self.authorize(&ctx, "read", "", Some(&p)).await?;
        let pr = self
            .engine
            .progress(&ctx, &p.id)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::GetProcessProgressResponse {
            progress: Some(pb::ProcessProgress {
                process_id: pr.process_id.clone(),
                methodology: pr.methodology.clone(),
                process: pr.process.clone(),
                description: pr.description.clone(),
                status: pr.status.to_string(),
                error: pr.error.clone(),
                steps: step_progress_to_pb(&pr.steps),
                done: pr.done as i32,
                total: pr.total as i32,
            }),
        }))
    }

    /// GetChangeState serves where each process of the methodology of a change stands now (ADR 0112): read like the
    /// change itself (a personal change is its subject's only).
    async fn get_change_state(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeStateRequest>,
    ) -> Result<Response<pb::GetChangeStateResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let ch = self.readable_change(&ctx, &r.msg.change_id).await?;
        let st = self
            .engine
            .change_state(&ctx, &ch.id)
            .await
            .map_err(to_connect)?;
        let mut out = pb::GetChangeStateResponse {
            change_id: s(&st.change_id),
            methodology: st.methodology.clone(),
            version: st.version.clone(),
            goal: st.goal.clone(),
            goal_reached: st.goal_reached,
            processes: Vec::new(),
        };
        for p in &st.processes {
            out.processes.push(pb::ProcessState {
                process: p.process.clone(),
                state: p.state.clone(),
                runs: p.runs as i32,
                current: p.current.clone(),
                status: p.status.to_string(),
                error: p.error.clone(),
                steps: step_states_to_pb(&p.steps),
                done: p.done as i32,
                total: p.total as i32,
            });
        }
        Ok(Response::new(out))
    }

    /// GetChangeAsks serves what a change waits for from people (ADR 0117): read like the change itself (a personal
    /// change is its subject's only). Whether the caller may answer an ask of a run also asks the permission the
    /// call answering it checks on the run (submit, decide_flow, relaunch).
    async fn get_change_asks(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeAsksRequest>,
    ) -> Result<Response<pb::GetChangeAsksResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let ch = self.readable_change(&ctx, &r.msg.change_id).await?;
        let asks = self
            .engine
            .change_asks(&ctx, &ch.id)
            .await
            .map_err(to_connect)?;
        let mut out = pb::GetChangeAsksResponse::default();
        for mut a in asks {
            if a.may_answer && !a.process_id.is_empty() {
                let act = answer_permission(&a.kind);
                if !act.is_empty() {
                    a.may_answer = self.load_authorized(&ctx, &a.process_id, act).await.is_ok();
                }
            }
            out.asks.push(pb::ChangeAsk {
                id: a.id,
                kind: a.kind,
                title: a.title,
                detail: a.detail,
                process_id: a.process_id,
                agent: a.agent,
                step: a.step,
                action: a.action,
                roles: a.roles,
                owner: a.owner,
                since: pbconv::time(&a.since),
                may_answer: a.may_answer,
                point: a.point,
                question: a.question,
            });
        }
        Ok(Response::new(out))
    }

    /// GetAssessmentStates serves how the assessment of each subject of a change reads now (ADR 0116): read like
    /// the change itself (a personal change is its subject's only).
    async fn get_assessment_states(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetAssessmentStatesRequest>,
    ) -> Result<Response<pb::GetAssessmentStatesResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let ch = self.readable_change(&ctx, &r.msg.change_id).await?;
        let states = self
            .engine
            .assessment_states(&ctx, &ch.id)
            .await
            .map_err(to_connect)?;
        let mut out = pb::GetAssessmentStatesResponse::default();
        for s in states {
            out.states.push(pb::AssessmentState {
                subject: s.subject,
                score: s.score as i32,
                verdict: s.verdict,
                current: s.current,
                answered: s.answered,
                tone: s.tone,
                label: s.label,
                open: s.open,
                errors: s.errors,
            });
        }
        Ok(Response::new(out))
    }

    /// ExplainCondition serves how a condition of a run's world state got its value (formula, parts, inputs).
    async fn explain_condition(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExplainConditionRequest>,
    ) -> Result<Response<pb::ExplainConditionResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let p = self
            .engine
            .store
            .get(&ctx, &m.process_id)
            .await
            .map_err(to_connect)?;
        self.authorize(&ctx, "read", "", Some(&p)).await?;
        let Some(ex) = self
            .engine
            .explain_condition(&ctx, &p.id, &m.condition)
            .await
            .map_err(to_connect)?
        else {
            return Err(ConnectError::not_found(format!(
                "condition {} is not defined by the methodology",
                go_quote(&m.condition)
            )));
        };
        Ok(Response::new(pb::ExplainConditionResponse {
            condition: ex.name.clone(),
            expr: ex.expr.clone(),
            value: ex.value,
            error: ex.error.clone(),
            inputs: ex.inputs.clone().into_iter().collect(),
            root: Some(term_to_pb(&ex.root)),
        }))
    }

    async fn list_processes(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListProcessesRequest>,
    ) -> Result<Response<pb::ListProcessesResponse>, ConnectError> {
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let me = authz::principal_from(&ctx);
        let all = self.engine.store.list(&ctx).await.map_err(to_connect)?;
        let statuses: HashSet<&str> = m.statuses.iter().map(String::as_str).collect();
        // only the processes the caller may read, filtered
        let mut out = pb::ListProcessesResponse::default();
        for p in &all {
            // or run on their behalf (ADR 0114)
            if (m.mine && p.initiator.subject != me.subject && p.initiator.end_user() != me.subject)
                || (!statuses.is_empty() && !statuses.contains(p.status.as_str()))
                || (m.roots_only && !p.parent_id.is_empty())
            {
                continue;
            }
            if self.authorize(&ctx, "read", "", Some(p)).await.is_ok() {
                out.processes.push(process_to_pb(p));
            }
        }
        Ok(Response::new(out))
    }

    /// WatchEvents streams live process events and logs.
    async fn watch_events(
        &self,
        ctx: &Ctx,
        r: Request<pb::WatchEventsRequest>,
    ) -> Result<Response<ServerStream<pb::WatchEventsResponse>>, ConnectError> {
        let Some(broker) = self.broker.clone() else {
            return Err(ConnectError::unimplemented(
                "event streaming is not configured",
            ));
        };
        let ctx = self.principal(ctx, &r.headers);
        let only = r.msg.process_id;
        if !only.is_empty() {
            self.load_authorized(&ctx, &only, "read").await?;
        }
        let (events, unsub) = broker.subscribe(256);
        let st = WatchState {
            h: self.clone(),
            ctx,
            only,
            events,
            _unsub: Unsub(Some(unsub)),
            allowed: HashMap::new(),
        };
        let stream = futures::stream::unfold(st, |mut st| async move {
            let out = st.next().await?;
            Some((Ok(out), st))
        });
        Ok(Response::new(goap_connect::server_stream(stream)))
    }

    /// ListTriggers lists the triggers of the published agents.
    async fn list_triggers(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListTriggersRequest>,
    ) -> Result<Response<pb::ListTriggersResponse>, ConnectError> {
        let mut out = pb::ListTriggersResponse::default();
        let Some(triggers) = &self.triggers else {
            return Ok(Response::new(out));
        };
        let ctx = self.principal(ctx, &r.headers);
        authz::check(
            &ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: authz::principal_from(&ctx),
                action: "read".into(),
                resource: Resource {
                    r#type: "trigger".into(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        for s in triggers.states() {
            out.triggers.push(pb::TriggerState {
                methodology: s.methodology,
                agent: s.agent,
                name: s.name,
                description: s.description,
                r#type: s.r#type,
                event: s.event,
                schedule: s.schedule,
                enabled: s.enabled,
                fires: s.fires as i32,
                last_fired: pbconv::opt_time(&s.last_fired),
                next_fire: pbconv::opt_time(&s.next_fire),
                last_process_id: s.last_process_id,
                last_error: s.last_error,
            });
        }
        Ok(Response::new(out))
    }

    /// FireTrigger fires a trigger now.
    async fn fire_trigger(
        &self,
        ctx: &Ctx,
        r: Request<pb::FireTriggerRequest>,
    ) -> Result<Response<pb::FireTriggerResponse>, ConnectError> {
        let Some(triggers) = &self.triggers else {
            return Err(ConnectError::unimplemented("triggers are disabled"));
        };
        let ctx = self.principal(ctx, &r.headers);
        let m = r.msg;
        let who = authz::principal_from(&ctx);
        authz::check(
            &ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: who.clone(),
                action: "fire".into(),
                resource: Resource {
                    r#type: "trigger".into(),
                    id: format!("{}/{}/{}", m.methodology, m.agent, m.trigger),
                    name: m.methodology.clone(),
                    org: who.org.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        let p = triggers
            .fire(&ctx, &m.methodology, &m.agent, &m.trigger)
            .await
            .map_err(|e| ConnectError::failed_precondition(e.to_string()))?;
        Ok(Response::new(pb::FireTriggerResponse {
            process: Some(process_to_pb(&p)),
        }))
    }
}

/// Ends a broker subscription when the stream goes.
struct Unsub(Option<engine::Unsubscribe>);

impl Drop for Unsub {
    fn drop(&mut self) {
        if let Some(u) = self.0.take() {
            u.cancel();
        }
    }
}

/// The state of a WatchEvents stream.
struct WatchState {
    h: Handler,
    ctx: Ctx,
    only: String,
    events: tokio::sync::mpsc::Receiver<engine::ProcessEvent>,
    _unsub: Unsub,
    /// read decision per process
    allowed: HashMap<String, bool>,
}

impl WatchState {
    fn related(&self, id: &str) -> bool {
        self.only.is_empty() || id == self.only
    }

    async fn can_read(&mut self, p: &Process) -> bool {
        if let Some(ok) = self.allowed.get(&p.id) {
            return *ok;
        }
        let ok = self
            .h
            .authorize(&self.ctx, "read", "", Some(p))
            .await
            .is_ok();
        self.allowed.insert(p.id.clone(), ok);
        ok
    }

    /// The next event to send; None ends the stream.
    async fn next(&mut self) -> Option<pb::WatchEventsResponse> {
        loop {
            let ev = tokio::select! {
                _ = self.ctx.done() => return None,
                ev = self.events.recv() => ev?,
            };
            let mut out = pb::WatchEventsResponse {
                r#type: ev.event.clone(),
                time: pbconv::time(&ev.time),
                ..Default::default()
            };
            if let Some(p) = &ev.process {
                if !(self.related(&p.id) || self.related(&p.parent_id)) || !self.can_read(p).await {
                    continue;
                }
                out.process = Some(process_to_pb(p));
            } else if let Some(l) = &ev.log {
                if !self.related(&l.process_id) {
                    continue;
                }
                match self.allowed.get(&l.process_id) {
                    Some(false) => continue,
                    Some(true) => {}
                    None => match self.h.engine.store.get(&self.ctx, &l.process_id).await {
                        Ok(p) if self.can_read(&p).await => {}
                        _ => continue,
                    },
                }
                out.log = Some(log_to_pb(l));
            } else {
                continue;
            }
            return Some(out);
        }
    }
}

/// answer_permission is the permission on the run the call answering an ask of that kind checks ("" for none: the
/// engine's own rules, already applied).
fn answer_permission(kind: &str) -> &'static str {
    match kind {
        engine::TASK_INPUT
        | engine::TASK_MERGE
        | engine::TASK_REBASE
        | engine::ASK_CLARIFICATION => "submit",
        engine::TASK_FLOW => "decide_flow",
        engine::TASK_BOARD => "relaunch",
        _ => "",
    }
}

/// StartingPointsToPB is the answer of ListStartingPoints: the steps the engine binds to the change.
pub fn starting_points_to_pb(sp: &engine::StartingPoints) -> pb::ListStartingPointsResponse {
    let mut out = pb::ListStartingPointsResponse {
        methodology: sp.methodology.clone(),
        version: sp.version.clone(),
        goal: sp.goal.clone(),
        reason: sp.reason.clone(),
        blocked_count: sp.blocked_count as i32,
        ..Default::default()
    };
    for p in &sp.points {
        out.points.push(pb::StartingPoint {
            id: p.id.clone(),
            kind: p.kind.clone(),
            parent: p.parent.clone(),
            method: p.method.clone(),
            why: p.why.clone(),
            may_run: p.may_run,
            need_roles: p.need_roles.clone(),
            running: p.running,
            launch: Some(pb::StartLaunch {
                methodology: p.launch.methodology.clone(),
                agent: p.launch.agent.clone(),
                goal: p.launch.goal.clone(),
                change_id: s(&p.launch.change_id),
            }),
        });
    }
    for b in &sp.blocked {
        out.blocked.push(pb::BlockedStep {
            id: b.id.clone(),
            missing: b.missing.clone(),
        });
    }
    out
}

fn step_states_to_pb(steps: &[engine::StepState]) -> Vec<pb::StepState> {
    steps
        .iter()
        .map(|s| pb::StepState {
            path: s.path.clone(),
            state: s.state.clone(),
            missing: s.missing.clone(),
            runs: s.runs as i32,
            child_process_ids: s.child_process_ids.clone(),
            lanes: lanes_to_pb(&s.lanes),
            waiting: s.waiting.clone(),
            permission: s.permission.clone(),
            chosen: s.chosen.clone(),
            steps: step_states_to_pb(&s.steps),
            needs: s
                .needs
                .iter()
                .map(|n| pb::NeedState {
                    condition: n.condition.clone(),
                    want: n.want,
                    requires: n.requires.clone(),
                    facts: facts_to_pb(&n.facts),
                })
                .collect(),
        })
        .collect()
}

fn needs_to_pb(needs: &[engine::Need]) -> Vec<pb::Need> {
    needs
        .iter()
        .map(|n| pb::Need {
            condition: n.condition.clone(),
            want: n.want,
            description: n.description.clone(),
            produced_by: n.produced_by.clone(),
            requires: needs_to_pb(&n.requires),
            facts: facts_to_pb(&n.facts),
        })
        .collect()
}

fn facts_to_pb(facts: &[engine::Fact]) -> Vec<pb::Fact> {
    facts
        .iter()
        .map(|f| pb::Fact {
            text: f.text.clone(),
            holds: f.holds,
            error: f.error.clone(),
            over: f.over.clone(),
            parts: facts_to_pb(&f.parts),
            missing: f.missing,
            produced_by: f.produced_by.clone(),
        })
        .collect()
}

fn term_to_pb(t: &goap_condition::Term) -> pb::ConditionTerm {
    pb::ConditionTerm {
        text: t.text.clone(),
        value: t.value.clone(),
        skipped: t.skipped,
        error: t.error.clone(),
        terms: t.terms.iter().map(term_to_pb).collect(),
    }
}

fn step_progress_to_pb(steps: &[engine::StepProgress]) -> Vec<pb::StepProgress> {
    steps
        .iter()
        .map(|s| pb::StepProgress {
            path: s.path.clone(),
            name: s.name.clone(),
            description: s.description.clone(),
            method: s.method.clone(),
            target: s.target.clone(),
            state: s.state.clone(),
            missing: s.missing.clone(),
            runs: s.runs as i32,
            child_process_ids: s.child_process_ids.clone(),
            waiting: s.waiting.clone(),
            permission: s.permission.clone(),
            guidance: s.guidance.clone(),
            checklist: s.checklist.clone(),
            references: doc_refs_to_pb(&s.references),
            steps: step_progress_to_pb(&s.steps),
            chosen: s.chosen.clone(),
            roles: resp_to_pb(s.roles.as_ref()),
            lanes: lanes_to_pb(&s.lanes),
            needs: needs_to_pb(&s.needs),
        })
        .collect()
}

fn lanes_to_pb(lanes: &[engine::Lane]) -> Vec<pb::Lane> {
    lanes
        .iter()
        .map(|l| pb::Lane {
            item: l.item.clone(),
            method: l.method.clone(),
            process_id: l.process_id.clone(),
            status: l.status.to_string(),
        })
        .collect()
}

fn step_context_to_pb(c: Option<&engine::StepContext>) -> Option<pb::StepContext> {
    let c = c?;
    Some(pb::StepContext {
        process: c.process.clone(),
        path: c.path.clone(),
        name: c.name.clone(),
        description: c.description.clone(),
        guidance: c.guidance.clone(),
        checklist: c.checklist.clone(),
        deliverables: c.deliverables.clone(),
        references: doc_refs_to_pb(&c.references),
        method: c.method.clone(),
        roles: resp_to_pb(c.roles.as_ref()),
    })
}

fn resp_to_pb(r: Option<&goap_methodology::Responsibilities>) -> Option<pb::Responsibilities> {
    let r = r?;
    Some(pb::Responsibilities {
        responsible: r.responsible.clone(),
        accountable: r.accountable.clone(),
        consulted: r.consulted.clone(),
        informed: r.informed.clone(),
    })
}

fn doc_refs_to_pb(refs: &[goap_methodology::Reference]) -> Vec<pb::DocumentReference> {
    refs.iter()
        .map(|r| pb::DocumentReference {
            title: r.title.clone(),
            r#ref: r.r#ref.clone(),
            section: r.section.clone(),
        })
        .collect()
}

/// LogToPB converts a log line of a process.
pub fn log_to_pb(l: &engine::LogLine) -> pb::LogLine {
    pb::LogLine {
        time: pbconv::time(&l.time),
        level: l.level.clone(),
        message: l.message.clone(),
        process_id: l.process_id.clone(),
        action: l.action.clone(),
        step: l.step as i32,
    }
}

fn usage_to_pb(u: &engine::Usage) -> pb::Usage {
    pb::Usage {
        input_tokens: u.input_tokens,
        output_tokens: u.output_tokens,
        llm_calls: u.llm_calls as i32,
        tool_calls: u.tool_calls as i32,
    }
}

/// ProcessToPB converts a process.
pub fn process_to_pb(p: &Process) -> pb::Process {
    let mut out = pb::Process {
        id: p.id.clone(),
        methodology: p.methodology.clone(),
        change_id: s(&p.change_id),
        status: p.status.to_string(),
        goal: p.goal.clone(),
        question: p.question.clone(),
        plan: p.plan.clone(),
        world: p.world.0.clone().into_iter().collect(),
        unknown: p.unknown.clone().into_iter().collect(),
        error: p.error.clone(),
        created_at: pbconv::time(&p.created_at),
        updated_at: pbconv::time(&p.updated_at),
        initiator: Some(pb::Principal {
            subject: p.initiator.subject.clone(),
            org: p.initiator.org.clone(),
            roles: p.initiator.roles.clone(),
            on_behalf_of: p.initiator.on_behalf_of.clone(),
        }),
        agent: p.agent.clone(),
        planner: p.planner.clone(),
        parent_id: p.parent_id.clone(),
        usage: Some(usage_to_pb(&p.usage)),
        baseline_id: s(&p.baseline_id),
        title: p.title.clone(),
        trace_id: p.trace_id.clone(),
        trigger: p.trigger.clone(),
        cause: p.cause.clone(),
        flow: p.flow.clone(),
        relaunch_of: p.relaunch_of.clone(),
        from_step: p.from_step as i32,
        instance: p.instance.clone(),
        closed: p.closed,
        runs: p.runs as i32,
        memory_turns: p.memory.len() as i32,
        ..Default::default()
    };
    for t in &p.intent.turns {
        out.turns.push(pb::Turn {
            role: t.role.clone(),
            text: t.text.clone(),
        });
    }
    for c in &p.candidates {
        out.candidates.push(pb::Candidate {
            goal: c.goal.clone(),
            confidence: c.confidence,
            reason: c.reason.clone(),
            agent: c.agent.clone(),
            methodology: c.methodology.clone(),
        });
    }
    if let Some(t) = &p.pending {
        let mut task = pb::HumanTask {
            kind: t.kind.clone(),
            permission: t.permission.clone(),
            action: t.action.clone(),
            description: t.description.clone(),
            instructions: t.instructions.clone(),
            node_types: t.node_types.clone(),
            step: t.step as i32,
            child_process_id: t.child_process_id.clone(),
            flow_id: t.flow_id.clone(),
            context: step_context_to_pb(t.context.as_ref()),
            conditions: t.conditions.clone(),
            form: pbconv::form_to_pb(t.form.as_ref()),
            ..Default::default()
        };
        for i in &t.issues {
            task.issues.push(pb::BoardIssue {
                item: i.item.to_string(),
                culprit: i.culprit.to_string(),
                code: i.code.clone(),
                message: i.message.clone(),
                severity: i.severity.clone(),
            });
        }
        if let Some(m) = &t.merge {
            task.merge = Some(merge_task_to_pb(m));
        }
        if let Some(rb) = &t.rebase {
            task.rebase = Some(pb::RebaseTask {
                branch: rb.branch.clone(),
                onto: rb.onto.clone(),
                impacts: rb
                    .impacts
                    .iter()
                    .map(|c| pb::RebaseConflict {
                        impact: c.impact.clone(),
                        key: c.key.clone(),
                        conflicts: c.conflicts.clone(),
                    })
                    .collect(),
            });
        }
        if let Some(pr) = &t.proposal {
            task.proposal = Some(pb::RelaunchProposal {
                process: pr.process.clone(),
                step: pr.step as i32,
                action: pr.action.clone(),
                reason: pr.reason.clone(),
                culprits: pr.culprits.iter().map(|c| c.to_string()).collect(),
            });
        }
        out.pending = Some(task);
    }
    // a BTreeMap: its keys come sorted
    out.disabled = p.disabled.keys().cloned().collect();
    for st in &p.steps {
        out.steps.push(pb::Step {
            index: st.index as i32,
            action: st.action.clone(),
            plan: st.plan.clone(),
            before: st.before.0.clone().into_iter().collect(),
            after: st.after.0.clone().into_iter().collect(),
            effects_met: st.effects_met,
            approved_by: st.approved_by.clone(),
            output: st.output.clone(),
            error: st.error.clone(),
            started_at: pbconv::time(&st.started_at),
            ended_at: pbconv::opt_time(&st.ended_at),
            items: st.items.iter().map(|i| i.to_string()).collect(),
            usage: Some(usage_to_pb(&st.usage)),
            child_process_ids: st.children.clone(),
            sandbox: st.sandbox.clone(),
            llm_calls: st
                .llm_calls
                .iter()
                .map(|c| pb::LlmCall {
                    provider: c.provider.clone(),
                    model: c.model.clone(),
                    input_tokens: c.input_tokens,
                    output_tokens: c.output_tokens,
                    duration_ms: c.duration_ms,
                    error: c.error.clone(),
                })
                .collect(),
            tool_calls: st
                .tool_calls
                .iter()
                .map(|c| pb::ToolCall {
                    name: c.name.clone(),
                    duration_ms: c.duration_ms,
                    error: c.error.clone(),
                })
                .collect(),
            logs: st.logs.iter().map(log_to_pb).collect(),
        });
    }
    out
}

fn merge_task_to_pb(m: &engine::MergeTask) -> pb::MergeTask {
    pb::MergeTask {
        namespace: m.namespace.clone(),
        from: m.from.clone(),
        into: m.into.clone(),
        conflicts: m
            .conflicts
            .iter()
            .map(|c| pb::MergeConflict {
                node: c.node.clone(),
                key: c.key.clone(),
                deleted: c.deleted,
                properties: c
                    .properties
                    .iter()
                    .map(|p| pb::MergeProperty {
                        name: p.name.clone(),
                        base: value(&p.base),
                        ours: value(&p.ours),
                        theirs: value(&p.theirs),
                        proposed: value(&p.proposed),
                    })
                    .collect(),
            })
            .collect(),
    }
}

/// value converts a property value (null for an absent one).
fn value(v: &Value) -> Option<pbjson_types::Value> {
    Some(pbconv::json_to_value(v))
}

/// Go's `%q` of a string (strconv.Quote, for the printable text of a condition name).
fn go_quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// `json.Marshal` of a value: `<`, `>`, `&`, U+2028 and U+2029 escaped the way Go escapes them.
pub(crate) fn go_json(v: &Value) -> String {
    let s = serde_json::to_string(v).unwrap_or_default();
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}
