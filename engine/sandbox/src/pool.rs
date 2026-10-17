use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_connect::{Interceptors, Request};
use goap_ctx::Ctx;
use goap_dsl as dsl;
use goap_engine::{Sandbox, Sandboxes};
use goap_gen::runtime::v1::{ExecuteRequest, SandboxService, SandboxServiceClient};
use parking_lot::Mutex;
use serde_json::{Map, Value};
use tokio::sync::watch;

use crate::runtime::{Runtime, serve};
use crate::{Error, with_ctx};

/// Spec describes a sandbox to start.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Spec {
    pub id: String,
    pub process_id: String,
    pub image: String,
    // Limits
    pub memory_mb: i64,
    pub cpus: f64,
    pub pids: i64,
}

/// Instance is a started sandbox.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Instance {
    pub id: String,
    /// base URL of its SandboxService
    pub endpoint: String,
}

/// Provisioner starts and stops sandboxes on an infrastructure.
#[async_trait]
pub trait Provisioner: Send + Sync {
    fn name(&self) -> &str;
    async fn start(&self, ctx: &Ctx, spec: Spec) -> Result<Instance, Error>;
    async fn stop(&self, ctx: &Ctx, id: &str) -> Result<(), Error>;
}

/// A started sandbox and its client.
struct Started {
    inst: Instance,
    client: SandboxServiceClient,
}

type Ready = Option<Result<Arc<Started>, String>>;

struct Boxed {
    last_used: Instant,
    ready: watch::Receiver<Ready>,
}

/// Pool implements [`Sandboxes`] on a Provisioner: one sandbox per process run, started on first use, stopped when
/// the process ends or after an idle period (a process waiting for a human does not keep its sandbox).
pub struct Pool {
    pub provisioner: Arc<dyn Provisioner>,
    pub runtime: Arc<Runtime>,
    /// RuntimeURL is the engine URL sandboxes call back.
    pub runtime_url: String,
    pub template: Spec,
    pub http: reqwest::Client,
    pub options: Interceptors,
    pub idle_ttl: Duration,
    pub timeout: Duration,
    boxes: Arc<Mutex<HashMap<String, Boxed>>>, // by process id
}

impl Pool {
    /// A pool over a provisioner, serving the hosts of its jobs through runtime (reached by the sandboxes at
    /// runtime_url).
    pub fn new(
        provisioner: Arc<dyn Provisioner>,
        runtime: Arc<Runtime>,
        runtime_url: &str,
    ) -> Self {
        Pool {
            provisioner,
            runtime,
            runtime_url: runtime_url.to_string(),
            template: Spec::default(),
            http: reqwest::Client::new(),
            options: Interceptors::none(),
            idle_ttl: Duration::ZERO,
            timeout: Duration::ZERO,
            boxes: Arc::default(),
        }
    }

    fn start(&self, process_id: &str, tx: watch::Sender<Ready>) {
        let prov = self.provisioner.clone();
        let http = self.http.clone();
        let options = self.options.clone();
        let mut spec = self.template.clone();
        let process_id = process_id.to_string();
        tokio::spawn(async move {
            let (ctx, _cancel) = Ctx::background().with_timeout(Duration::from_secs(120));
            spec.id = format!("goap-sbx-{}", &uuid::Uuid::new_v4().to_string()[..8]);
            spec.process_id = process_id.clone();
            let res = match prov.start(&ctx, spec).await {
                Err(e) => Err(format!("{}: start sandbox: {e}", prov.name())),
                Ok(inst) => match wait_ready(&ctx, &http, &inst.endpoint).await {
                    Err(e) => {
                        let _ = prov.stop(&Ctx::background(), &inst.id).await;
                        Err(format!(
                            "{}: sandbox {} not ready: {e}",
                            prov.name(),
                            inst.id
                        ))
                    }
                    Ok(()) => {
                        let client = goap_connect::Client::builder(&inst.endpoint)
                            .http(http.clone())
                            .interceptors(&options)
                            .build();
                        tracing::info!(provisioner = prov.name(), sandbox = %inst.id, process = %process_id, "sandbox started");
                        Ok(Arc::new(Started {
                            inst,
                            client: SandboxServiceClient::new(client),
                        }))
                    }
                },
            };
            let _ = tx.send(Some(res));
        });
    }

    /// The instance of the sandbox of a process, once started.
    pub fn instance(&self, process_id: &str) -> Option<Instance> {
        let boxes = self.boxes.lock();
        let r = boxes.get(process_id)?.ready.borrow();
        match &*r {
            Some(Ok(s)) => Some(s.inst.clone()),
            _ => None,
        }
    }

    /// ReapIdle stops the sandboxes unused for IdleTTL (call periodically).
    pub fn reap_idle(&self) {
        let ttl = if self.idle_ttl.is_zero() {
            Duration::from_secs(600)
        } else {
            self.idle_ttl
        };
        let idle: Vec<String> = self
            .boxes
            .lock()
            .iter()
            .filter(|(_, b)| b.last_used.elapsed() > ttl)
            .map(|(id, _)| id.clone())
            .collect();
        for id in idle {
            self.release_now(&id);
        }
    }

    /// Close stops every sandbox.
    pub fn close(&self) {
        let ids: Vec<String> = self.boxes.lock().keys().cloned().collect();
        for id in ids {
            self.release_now(&id);
        }
    }

    fn release_now(&self, process_id: &str) {
        let Some(b) = self.boxes.lock().remove(process_id) else {
            return;
        };
        let prov = self.provisioner.clone();
        let process_id = process_id.to_string();
        let mut ready = b.ready;
        tokio::spawn(async move {
            let r: Ready = match ready.wait_for(|v| v.is_some()).await {
                Ok(r) => r.clone(),
                Err(_) => return,
            };
            let Some(Ok(s)) = r else {
                return;
            };
            match prov.stop(&Ctx::background(), &s.inst.id).await {
                Err(e) => tracing::warn!(sandbox = %s.inst.id, err = %e, "sandbox stop"),
                Ok(()) => {
                    tracing::info!(sandbox = %s.inst.id, process = %process_id, "sandbox stopped")
                }
            }
        });
    }
}

async fn wait_ready(ctx: &Ctx, hc: &reqwest::Client, endpoint: &str) -> Result<(), Error> {
    let url = format!("{endpoint}/healthz");
    loop {
        let last = match with_ctx(ctx, hc.get(&url).send()).await {
            Ok(resp) if resp.status() == reqwest::StatusCode::OK => return Ok(()),
            Ok(resp) => Error::msg(format!("healthz: {}", resp.status())),
            Err(e) => e,
        };
        tokio::select! {
            e = ctx.done() => {
                return Err(match last {
                    Error::Ctx(_) => Error::Ctx(e),
                    other => other,
                });
            }
            _ = tokio::time::sleep(Duration::from_millis(200)) => {}
        }
    }
}

#[async_trait]
impl Sandboxes for Pool {
    async fn acquire(&self, ctx: &Ctx, process_id: &str) -> goap_engine::Result<Arc<dyn Sandbox>> {
        let mut ready = {
            let mut boxes = self.boxes.lock();
            let b = match boxes.get_mut(process_id) {
                Some(b) => b,
                None => {
                    let (tx, rx) = watch::channel(None);
                    boxes.insert(
                        process_id.to_string(),
                        Boxed {
                            last_used: Instant::now(),
                            ready: rx,
                        },
                    );
                    self.start(process_id, tx);
                    boxes.get_mut(process_id).expect("just inserted")
                }
            };
            b.last_used = Instant::now();
            b.ready.clone()
        };
        let got = tokio::select! {
            r = ready.wait_for(|v| v.is_some()) => r.ok().and_then(|v| v.clone()),
            e = ctx.done() => return Err(goap_engine::Error::Ctx(e)),
        };
        match got {
            Some(Ok(s)) => Ok(Arc::new(Remote {
                runtime: self.runtime.clone(),
                runtime_url: self.runtime_url.clone(),
                timeout: self.timeout,
                started: s,
            })),
            Some(Err(e)) => {
                self.boxes.lock().remove(process_id);
                Err(goap_engine::Error::msg(e))
            }
            None => {
                self.boxes.lock().remove(process_id);
                Err(goap_engine::Error::msg("sandbox start abandoned"))
            }
        }
    }

    async fn release(&self, _ctx: &Ctx, process_id: &str) {
        self.release_now(process_id);
    }
}

struct Remote {
    runtime: Arc<Runtime>,
    runtime_url: String,
    timeout: Duration,
    started: Arc<Started>,
}

fn script_error(msg: String) -> dsl::Error {
    match msg.strip_prefix("script interrupted: ") {
        Some(cause) => dsl::Error::Interrupted(cause.to_string()),
        None => dsl::Error::Script(msg),
    }
}

fn from_timestamp(t: Option<&pbjson_types::Timestamp>) -> DateTime<Utc> {
    t.and_then(|t| DateTime::from_timestamp(t.seconds, u32::try_from(t.nanos).unwrap_or(0)))
        .unwrap_or_default()
}

#[async_trait]
impl Sandbox for Remote {
    fn id(&self) -> String {
        self.started.inst.id.clone()
    }

    /// Execute sends the job to the sandbox; the host is reachable from it through RuntimeService for the duration
    /// of the job only.
    async fn execute(
        &self,
        ctx: &Ctx,
        job: dsl::Job,
        host: &dyn dsl::Host,
    ) -> (dsl::ScriptResult, Result<(), dsl::Error>) {
        let job_id = uuid::Uuid::new_v4().to_string();
        let (token, unregister, mut calls) = self.runtime.bridge(&job_id);
        let mut snapshot = Map::new();
        snapshot.insert(
            "items".into(),
            serde_json::to_value(&job.items).unwrap_or(Value::Null),
        );
        snapshot.insert(
            "nodes".into(),
            serde_json::to_value(&job.nodes).unwrap_or(Value::Null),
        );
        snapshot.insert("intent".into(), Value::String(job.intent.clone()));
        snapshot.insert("goal".into(), Value::String(job.goal.clone()));
        let timeout = if self.timeout.is_zero() {
            dsl::DEFAULT_TIMEOUT
        } else {
            self.timeout
        };
        let req = ExecuteRequest {
            job_id: job_id.clone(),
            token,
            runtime_url: self.runtime_url.clone(),
            language: job.language.clone(),
            code: job.code.clone(),
            process_id: job.process_id.clone(),
            agent: job.agent.clone(),
            action: job.action.clone(),
            params_json: serde_json::to_string(&job.params).unwrap_or_default(),
            vars_json: serde_json::to_string(&job.vars).unwrap_or_default(),
            blackboard_json: Value::Object(snapshot).to_string(),
            timeout_ms: i64::try_from(timeout.as_millis()).unwrap_or(i64::MAX),
            ..Default::default()
        };
        let call = self.started.client.execute(ctx, Request::new(req));
        tokio::pin!(call);
        // the host is borrowed: its calls are served here while the sandbox runs the job
        let resp = loop {
            tokio::select! {
                r = &mut call => break r,
                Some(c) = calls.recv() => {
                    let out = serve(ctx, host, &c.op, &c.args).await;
                    let _ = c.reply.send(out);
                }
            }
        };
        drop(unregister);
        let m = match resp {
            Ok(r) => r.into_inner(),
            Err(e) => {
                return (
                    dsl::ScriptResult::default(),
                    Err(dsl::Error::host(format!(
                        "sandbox {}: {e}",
                        self.started.inst.id
                    ))),
                );
            }
        };
        let mut res = dsl::ScriptResult {
            output: m.output,
            suspended: m.suspended,
            ..Default::default()
        };
        for l in &m.logs {
            res.logs.push(dsl::LogLine {
                time: from_timestamp(l.time.as_ref()),
                level: l.level.clone(),
                message: l.message.clone(),
            });
        }
        if !m.error.is_empty() {
            return (res, Err(script_error(m.error)));
        }
        if !m.items_json.is_empty() {
            match serde_json::from_str::<Option<Vec<Map<String, Value>>>>(&m.items_json) {
                Ok(v) => res.items = v.unwrap_or_default(),
                Err(e) => return (res, Err(dsl::Error::host(format!("sandbox items: {e}")))),
            }
        }
        if !m.nodes_json.is_empty() {
            match serde_json::from_str::<Option<Vec<dsl::NodeOp>>>(&m.nodes_json) {
                Ok(v) => res.nodes = v.unwrap_or_default(),
                Err(e) => {
                    return (
                        res,
                        Err(dsl::Error::host(format!("sandbox change impacts: {e}"))),
                    );
                }
            }
        }
        (res, Ok(()))
    }
}
