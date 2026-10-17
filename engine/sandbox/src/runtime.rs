use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::{Code, ConnectError, Request, Response};
use goap_ctx::Ctx;
use goap_dsl as dsl;
use goap_gen::runtime::v1::{CallRequest, CallResponse, RuntimeService, RuntimeServiceClient};
use parking_lot::RwLock;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tokio::sync::{mpsc, oneshot};

/// A call of a job served by the action waiting for the sandbox (see [`Runtime::bridge`]).
pub(crate) struct BridgedCall {
    pub(crate) op: String,
    pub(crate) args: String,
    pub(crate) reply: oneshot::Sender<CallResponse>,
}

#[derive(Clone)]
enum Target {
    /// A host shared with the runtime (Go's Register).
    Host(Arc<dyn dsl::Host>),
    /// A host borrowed by the action: the calls go to the task that runs it.
    Bridge(mpsc::Sender<BridgedCall>),
}

#[derive(Clone)]
struct Registered {
    token: String,
    target: Target,
}

/// Runtime is the engine side RuntimeService: it maps job tokens to the host of the running action. A sandbox can
/// only reach the host of its own job.
#[derive(Default)]
pub struct Runtime {
    jobs: Arc<RwLock<HashMap<String, Registered>>>,
}

/// Unregister revokes the token of a job: when [`Unregister::done`] is called or when it is dropped.
pub struct Unregister {
    jobs: Arc<RwLock<HashMap<String, Registered>>>,
    job_id: String,
}

impl Unregister {
    pub fn done(self) {}
}

impl Drop for Unregister {
    fn drop(&mut self) {
        self.jobs.write().remove(&self.job_id);
    }
}

fn new_token() -> String {
    // 32 random bytes in Go; two v4 UUIDs here (244 random bits), hex encoded
    format!(
        "{}{}",
        uuid::Uuid::new_v4().simple(),
        uuid::Uuid::new_v4().simple()
    )
}

fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

impl Runtime {
    /// NewRuntime returns an empty runtime registry.
    pub fn new() -> Self {
        Self::default()
    }

    fn put(&self, job_id: &str, target: Target) -> (String, Unregister) {
        let token = new_token();
        self.jobs.write().insert(
            job_id.to_string(),
            Registered {
                token: token.clone(),
                target,
            },
        );
        (
            token,
            Unregister {
                jobs: self.jobs.clone(),
                job_id: job_id.to_string(),
            },
        )
    }

    /// Register exposes host for a job and returns its token.
    pub fn register(&self, job_id: &str, host: Arc<dyn dsl::Host>) -> (String, Unregister) {
        self.put(job_id, Target::Host(host))
    }

    /// Bridge exposes the host of an action that borrows it: the calls of the job arrive on the receiver, for the
    /// action to serve while it waits for the sandbox.
    pub(crate) fn bridge(&self, job_id: &str) -> (String, Unregister, mpsc::Receiver<BridgedCall>) {
        let (tx, rx) = mpsc::channel(1);
        let (token, un) = self.put(job_id, Target::Bridge(tx));
        (token, un, rx)
    }

    /// The number of jobs registered now.
    pub fn jobs(&self) -> usize {
        self.jobs.read().len()
    }

    fn target(&self, job_id: &str, token: &str) -> Result<Target, ConnectError> {
        let j = self.jobs.read().get(job_id).cloned();
        match j {
            Some(j) if constant_time_eq(j.token.as_bytes(), token.as_bytes()) => Ok(j.target),
            _ => Err(ConnectError::new(
                Code::Unauthenticated,
                "unknown job or bad token",
            )),
        }
    }
}

#[async_trait]
impl RuntimeService for Runtime {
    async fn call(
        &self,
        ctx: &Ctx,
        req: Request<CallRequest>,
    ) -> Result<Response<CallResponse>, ConnectError> {
        let m = req.into_inner();
        match self.target(&m.job_id, &m.token)? {
            Target::Host(h) => Ok(Response::new(
                serve(ctx, h.as_ref(), &m.op, &m.args_json).await,
            )),
            Target::Bridge(tx) => {
                let (reply, rx) = oneshot::channel();
                let call = BridgedCall {
                    op: m.op,
                    args: m.args_json,
                    reply,
                };
                if tx.send(call).await.is_err() {
                    return Err(ConnectError::new(
                        Code::Unauthenticated,
                        "unknown job or bad token",
                    ));
                }
                rx.await
                    .map(Response::new)
                    .map_err(|_| ConnectError::new(Code::Aborted, "the job ended"))
            }
        }
    }
}

/// The answer of the host to one call of a job.
pub(crate) async fn serve(ctx: &Ctx, host: &dyn dsl::Host, op: &str, args: &str) -> CallResponse {
    let (result, err) = dispatch(ctx, host, op, args.as_bytes()).await;
    let mut out = CallResponse::default();
    match err {
        Some(e) if e.is_suspended() => out.suspended = true,
        Some(e) => out.error = e.to_string(),
        None => {}
    }
    if let Some(v) = result {
        out.result_json = serde_json::to_string(&v).unwrap_or_default();
    }
    out
}

/// The arguments of a runtime call (every field is sent, as Go marshals them).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
struct CallArgs {
    request: dsl::CompleteRequest,
    name: String,
    intent: String,
    args: Option<Map<String, Value>>,
    key: String,
    r#type: String,
    direction: String,
}

fn to_value<T: Serialize>(v: Result<T, dsl::Error>) -> (Option<Value>, Option<dsl::Error>) {
    match v {
        Ok(v) => (serde_json::to_value(v).ok(), None),
        Err(e) => (None, Some(e)),
    }
}

async fn dispatch(
    ctx: &Ctx,
    h: &dyn dsl::Host,
    op: &str,
    raw: &[u8],
) -> (Option<Value>, Option<dsl::Error>) {
    let mut a = CallArgs::default();
    if !raw.is_empty() {
        match serde_json::from_slice(raw) {
            Ok(v) => a = v,
            Err(e) => {
                return (
                    None,
                    Some(dsl::Error::host(format!("{op}: bad arguments: {e}"))),
                );
            }
        }
    }
    match op {
        "llm.complete" => to_value(h.complete(ctx, a.request).await),
        "agents.run" => to_value(h.run_agent(ctx, &a.name, &a.intent).await),
        "tools.call" => to_value(h.call_tool(ctx, &a.name, a.args.unwrap_or_default()).await),
        "domain.node" => to_value(h.node(ctx, &a.key).await),
        "domain.nodes" => to_value(h.nodes(ctx, &a.r#type).await),
        "domain.links" => to_value(h.links(ctx, &a.key, &a.direction, &a.r#type).await),
        _ => (
            None,
            Some(dsl::Error::host(format!(
                "unknown runtime operation {op:?}"
            ))),
        ),
    }
}

/// RemoteHost is the sandbox side [`dsl::Host`]: every call goes to the engine RuntimeService with the job token.
#[derive(Clone)]
pub struct RemoteHost {
    pub client: RuntimeServiceClient,
    pub job_id: String,
    pub token: String,
}

impl RemoteHost {
    async fn call<T: DeserializeOwned + Default>(
        &self,
        ctx: &Ctx,
        op: &str,
        args: CallArgs,
    ) -> Result<T, dsl::Error> {
        let raw = serde_json::to_string(&args).unwrap_or_default();
        let r = self
            .client
            .call(
                ctx,
                Request::new(CallRequest {
                    job_id: self.job_id.clone(),
                    token: self.token.clone(),
                    op: op.to_string(),
                    args_json: raw,
                }),
            )
            .await
            .map_err(dsl::Error::other)?
            .into_inner();
        let mut out = T::default();
        if !r.result_json.is_empty() {
            let v: Value = serde_json::from_str(&r.result_json).map_err(dsl::Error::other)?;
            if !v.is_null() {
                out = serde_json::from_value(v).map_err(dsl::Error::other)?;
            }
        }
        if r.suspended {
            return Err(dsl::Error::Suspended);
        }
        if !r.error.is_empty() {
            return Err(dsl::Error::host(r.error));
        }
        Ok(out)
    }
}

#[async_trait]
impl dsl::Host for RemoteHost {
    async fn complete(
        &self,
        ctx: &Ctx,
        req: dsl::CompleteRequest,
    ) -> Result<dsl::CompleteResult, dsl::Error> {
        self.call(
            ctx,
            "llm.complete",
            CallArgs {
                request: req,
                ..Default::default()
            },
        )
        .await
    }

    async fn run_agent(
        &self,
        ctx: &Ctx,
        name: &str,
        intent: &str,
    ) -> Result<dsl::AgentResult, dsl::Error> {
        self.call(
            ctx,
            "agents.run",
            CallArgs {
                name: name.into(),
                intent: intent.into(),
                ..Default::default()
            },
        )
        .await
    }

    async fn call_tool(
        &self,
        ctx: &Ctx,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value, dsl::Error> {
        self.call(
            ctx,
            "tools.call",
            CallArgs {
                name: name.into(),
                args: Some(args),
                ..Default::default()
            },
        )
        .await
    }

    async fn node(&self, ctx: &Ctx, key: &str) -> Result<dsl::Node, dsl::Error> {
        self.call(
            ctx,
            "domain.node",
            CallArgs {
                key: key.into(),
                ..Default::default()
            },
        )
        .await
    }

    async fn nodes(&self, ctx: &Ctx, node_type: &str) -> Result<Vec<dsl::Node>, dsl::Error> {
        self.call(
            ctx,
            "domain.nodes",
            CallArgs {
                r#type: node_type.into(),
                ..Default::default()
            },
        )
        .await
    }

    async fn links(
        &self,
        ctx: &Ctx,
        key: &str,
        direction: &str,
        link_type: &str,
    ) -> Result<Vec<dsl::Link>, dsl::Error> {
        self.call(
            ctx,
            "domain.links",
            CallArgs {
                key: key.into(),
                direction: direction.into(),
                r#type: link_type.into(),
                ..Default::default()
            },
        )
        .await
    }
}
