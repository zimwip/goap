//! The goap-scheduler connector (`scheduler.go`).

use std::collections::HashMap;

use goap_authz::Principal;
use goap_connectorkit::{Connector, InvokeError};
use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeID};
use goap_gen::connector::v1::ConnectorInfo;
use goap_gen::engine::v1::{
    AttachChangeRequest, FireTriggerRequest, GetProcessRequest, ListProcessesRequest,
    ListStartingPointsRequest, ListStartingPointsResponse, ListTriggersRequest, Process,
    StartProcessRequest,
};
use goap_gen::registry::v1::GetMethodologyViewRequest;
use goap_mcpbuiltin as mcpbuiltin;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::admin::in_force;
use crate::builtin::{
    Args, EngineAPI, Error, Op, Ports, boxed, caller, info, op, request, result, schema, unknown,
};

/// Scheduler is the goap-scheduler connector: it starts processes (an agent working on a change for an intent, like
/// a harness hands a task to a sub-agent), follows them and fires triggers.
#[derive(Clone)]
pub struct Scheduler {
    p: Ports,
}

fn scheduler_ops() -> Vec<Op> {
    vec![
        op(
            "start",
            "Start a process: {process}",
            schema(
                &[
                    ("intent", "string"),
                    ("methodology", "string"),
                    ("agent", "string"),
                    ("goal", "string"),
                    ("title", "string"),
                    ("change", "string"),
                    ("namespace", "string"),
                    ("unit", "string"),
                    ("vars", "object"),
                ],
                &["intent"],
            ),
        ),
        op(
            "attach",
            "Bind a process (default: the caller's own) to a change, existing (change) or new: {process}",
            schema(
                &[
                    ("process", "string"),
                    ("change", "string"),
                    ("title", "string"),
                    ("intent", "string"),
                    ("namespace", "string"),
                    ("unit", "string"),
                    ("baseline", "string"),
                ],
                &[],
            ),
        ),
        op(
            "list",
            "List processes, the latest first: {processes, truncated}",
            schema(
                &[
                    ("mine", "boolean"),
                    ("status", "string"),
                    ("limit", "integer"),
                ],
                &[],
            ),
        ),
        op(
            "get",
            "Read a process: {process}",
            schema(&[("id", "string")], &["id"]),
        ),
        op(
            "starting_points",
            "The steps of the methodology of a change that are possible now towards its goal, with what makes each possible and how to start it; steps that wait are only counted: {methodology, goal, reason, points, blocked, blockedCount}",
            schema(&[("changeId", "string"), ("methodology", "string")], &[]),
        ),
        op(
            "triggers",
            "List the triggers: {triggers}",
            schema(&[], &[]),
        ),
        op(
            "fire",
            "Fire a trigger: {process}",
            schema(
                &[
                    ("methodology", "string"),
                    ("agent", "string"),
                    ("trigger", "string"),
                ],
                &["methodology", "agent", "trigger"],
            ),
        ),
    ]
}

/// processView keeps what a tool caller needs of a process; the steps only in detail.
pub(crate) fn process_view(p: Option<Process>, detail: bool) -> Option<Process> {
    let mut v = p?;
    v.world.clear();
    v.unknown.clear();
    v.candidates.clear();
    if !detail {
        v.steps.clear();
        v.turns.clear();
    }
    Some(v)
}

/// protoMap is a message in the protobuf JSON form (camelCase names, unpopulated fields left out).
pub(crate) fn proto_map(m: &impl Serialize) -> Result<Map<String, Value>, Error> {
    result(m)
}

/// protoResult returns a message as the result, under key when not empty.
pub(crate) fn proto_result<M: Serialize>(
    key: &str,
    m: Option<&M>,
) -> Result<Map<String, Value>, Error> {
    let Some(m) = m else {
        return Err(Error::msg(format!("the engine returned no {key}")));
    };
    let v = proto_map(m)?;
    if key.is_empty() {
        return Ok(v);
    }
    let mut out = Map::new();
    out.insert(key.into(), Value::Object(v));
    Ok(out)
}

/// The time a process was created, for ordering (Go's AsTime of a missing timestamp: the Unix epoch).
fn created(p: &Process) -> (i64, i32) {
    p.created_at
        .as_ref()
        .map_or((0, 0), |t| (t.seconds, t.nanos))
}

impl Scheduler {
    pub fn new(p: Ports) -> Self {
        Scheduler { p }
    }

    fn engine(&self) -> Result<&EngineAPI, Error> {
        self.p
            .engine
            .as_deref()
            .ok_or_else(|| Error::msg("goap-scheduler has no engine"))
    }

    /// Invoke without the boxing of the connector's error: the operation's result or its typed error.
    pub async fn call(
        &self,
        ctx: &Ctx,
        op: &str,
        raw: &Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let who = caller(ctx)?;
        let a = Args(raw);
        match op {
            "start" => {
                let intent = a.required("intent")?;
                let mut req = StartProcessRequest {
                    intent: intent.clone(),
                    methodology: a.str("methodology"),
                    agent: a.str("agent"),
                    goal: a.str("goal"),
                    title: a.str("title"),
                    change_id: a.str("change"),
                    owner_org: a.unit(ctx, &who),
                    vars: goap_pbconv::to_struct(&a.object("vars")),
                    ..Default::default()
                };
                if req.title.is_empty() {
                    req.title = intent;
                }
                if req.change_id.is_empty() {
                    req.baseline_id = self.baseline(ctx, a).await?;
                }
                let r = self
                    .engine()?
                    .start_process(ctx, request(&who, req))
                    .await?;
                proto_result("process", process_view(r.msg.process, false).as_ref())
            }
            "attach" => {
                let mut process = a.str("process");
                if process.is_empty() {
                    process = goap_mcp::call_from(ctx).process;
                }
                if process.is_empty() {
                    return Err(Error::msg(
                        r#"argument "process" is required outside a process"#,
                    ));
                }
                let req = AttachChangeRequest {
                    process_id: process,
                    change_id: a.str("change"),
                    title: a.str("title"),
                    intent: a.str("intent"),
                    namespace: a.str("namespace"),
                    baseline_id: a.str("baseline"),
                    owner_org: a.unit(ctx, &who),
                    ..Default::default()
                };
                let r = self
                    .engine()?
                    .attach_change(ctx, request(&who, req))
                    .await?;
                proto_result("process", process_view(r.msg.process, false).as_ref())
            }
            "get" => {
                let id = a.required("id")?;
                let r = self
                    .engine()?
                    .get_process(ctx, request(&who, GetProcessRequest { id }))
                    .await?;
                proto_result("process", process_view(r.msg.process, true).as_ref())
            }
            "list" => {
                let mut req = ListProcessesRequest {
                    mine: a.boolean("mine"),
                    ..Default::default()
                };
                let st = a.str("status");
                if !st.is_empty() {
                    req.statuses = vec![st];
                }
                let r = self
                    .engine()?
                    .list_processes(ctx, request(&who, req))
                    .await?;
                let mut ps = r.msg.processes;
                ps.sort_by_key(|p| std::cmp::Reverse(created(p)));
                let truncated = ps.len() > a.limit();
                if truncated {
                    ps.truncate(a.limit());
                }
                let mut list = Vec::new();
                for p in ps {
                    if let Some(v) = process_view(Some(p), false) {
                        list.push(Value::Object(proto_map(&v)?));
                    }
                }
                let mut out = Map::new();
                out.insert("processes".into(), Value::Array(list));
                out.insert("truncated".into(), Value::Bool(truncated));
                Ok(out)
            }
            "starting_points" => {
                let mut id = a.str("changeId");
                if id.is_empty() {
                    id = goap_mcp::call_from(ctx).change;
                }
                if id.is_empty() {
                    return Err(Error::msg(
                        r#"argument "changeId" is required outside a change"#,
                    ));
                }
                let r = self
                    .engine()?
                    .list_starting_points(
                        ctx,
                        request(
                            &who,
                            ListStartingPointsRequest {
                                change_id: id,
                                methodology: a.str("methodology"),
                                ..Default::default()
                            },
                        ),
                    )
                    .await?;
                self.starting_points(ctx, &who, &r.msg).await
            }
            "triggers" => {
                let r = self
                    .engine()?
                    .list_triggers(ctx, request(&who, ListTriggersRequest::default()))
                    .await?;
                proto_result("", Some(&r.msg))
            }
            "fire" => {
                let (m, ag, tr) = (a.str("methodology"), a.str("agent"), a.str("trigger"));
                if m.is_empty() || ag.is_empty() || tr.is_empty() {
                    return Err(Error::msg(
                        r#"arguments "methodology", "agent" and "trigger" are required"#,
                    ));
                }
                let r = self
                    .engine()?
                    .fire_trigger(
                        ctx,
                        request(
                            &who,
                            FireTriggerRequest {
                                methodology: m,
                                agent: ag,
                                trigger: tr,
                            },
                        ),
                    )
                    .await?;
                proto_result("process", process_view(r.msg.process, false).as_ref())
            }
            _ => Err(unknown(op)),
        }
    }

    /// startingPoints composes the points the engine bound with the view of their methodology version (ADR 0126 step
    /// 5): what each step is comes from the registry, what is possible now from the engine.
    async fn starting_points(
        &self,
        ctx: &Ctx,
        who: &Principal,
        b: &ListStartingPointsResponse,
    ) -> Result<Map<String, Value>, Error> {
        let mut view = None;
        if let Some(m) = self.p.methodologies.as_deref()
            && b.points.len() + b.blocked.len() > 0
        {
            let r = m
                .get_methodology_view(
                    ctx,
                    request(
                        who,
                        GetMethodologyViewRequest {
                            name: b.methodology.clone(),
                            version: b.version.clone(),
                            ..Default::default()
                        },
                    ),
                )
                .await
                .map_err(|e| Error::Wrapped {
                    message: format!(
                        "the view of methodology {} {} cannot be read",
                        b.methodology, b.version
                    ),
                    source: Box::new(Error::Rpc(e)),
                })?;
            view = r.msg.view;
        }
        result(goap_startingpoint::compose(view.as_ref(), b))
    }

    /// baseline is the head of main of the namespace a new process works on: the one the call names, else the one of
    /// its methodology, else the one of the calling change.
    async fn baseline(&self, ctx: &Ctx, a: Args<'_>) -> Result<String, Error> {
        let mut ns = a.str("namespace");
        let m = a.str("methodology");
        if let Some(reg) = self.p.methodologies.as_deref()
            && ns.is_empty()
            && !m.is_empty()
        {
            for c in in_force(ctx, reg).await? {
                if c.name == m {
                    ns = c.namespace;
                }
            }
        }
        let id = goap_mcp::call_from(ctx).change;
        if let Some(g) = self.p.graph.as_deref()
            && ns.is_empty()
            && !id.is_empty()
        {
            let bb = g.blackboard(ctx, &ChangeID::from(id)).await?;
            ns = bb.change.namespace;
        }
        let Some(g) = self.p.graph.as_deref().filter(|_| !ns.is_empty()) else {
            return Err(Error::msg(
                "name the namespace (or the methodology, or the change) the process works on",
            ));
        };
        let head = g
            .branch_head(ctx, &ns, domain::MAIN_BRANCH)
            .await
            .map_err(|e| e.context(format!("head of main of {ns}")))?;
        Ok(head.id.to_string())
    }
}

#[async_trait::async_trait]
impl Connector for Scheduler {
    fn info(&self) -> ConnectorInfo {
        info(
            mcpbuiltin::SCHEDULER,
            "Processes and triggers of the engine, run for the caller (built in).",
            &scheduler_ops(),
        )
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        _config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        self.call(ctx, op, &args).await.map_err(boxed)
    }
}
