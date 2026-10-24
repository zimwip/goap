//! The long runs of the graph as jobs of the job service (Go's `graphsvc/jobs.go`, ADR 0149): `StartGraphExport` and
//! `StartGraphImport` create the job for the person who asked, and the worker of this process claims the graph's kinds
//! and runs them as that person (`lib/jobkit.Worker`), reporting their progress, their log and their result.
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex as StdMutex},
    time::Duration,
};

use super::*;
use goap_proto::jobs::v1 as jobspb;
use goap_proto::jobs::v1::JobService as _;
use serde::{Deserialize, Serialize};
use serde_json::Value as Json;

/// The job kinds of the graph (`graphsvc.KindGraphExport`, `KindGraphImport`).
pub const KIND_EXPORT: &str = "graph.export";
pub const KIND_IMPORT: &str = "graph.import";

const FORMAT_JSON: &str = "json";
const FORMAT_OKF: &str = "okf";

/// The header of the services' token on their calls to the job service (`jobkit.TokenHeader`).
const TOKEN_HEADER: &str = "X-Goap-Job-Token";
/// The size of the chunks a result is written in (`jobkit.ResultChunk`).
const RESULT_CHUNK: usize = 512 << 10;
/// The lines of feedback a job keeps between two reports (`jobkit.MaxLog`).
const MAX_LOG: usize = 200;
/// How often a running job reports when it changed, and when it did not (`jobkit.ReportEvery`, `Heartbeat`).
const REPORT_EVERY: Duration = Duration::from_millis(500);
const HEARTBEAT: Duration = Duration::from_secs(2);
/// How long a claimed job holds without a report (`jobkit.DefaultLease`).
const DEFAULT_LEASE: Duration = Duration::from_secs(30);

/// The job service as the graph calls it (`jobkit.Client`), with the services' token.
pub struct Jobs {
    pub(super) client: jobspb::JobServiceClient,
}

impl Jobs {
    /// The client of the job service at `GOAP_JOBS_URL` (default `http://localhost:8091`) with `GOAP_JOB_TOKEN`, as
    /// `platform/cmd/graph` makes it.
    pub fn from_env() -> Jobs {
        let url = std::env::var("GOAP_JOBS_URL")
            .ok()
            .filter(|u| !u.is_empty())
            .unwrap_or_else(|| "http://localhost:8091".into());
        let token = std::env::var("GOAP_JOB_TOKEN").unwrap_or_default();
        let mut b = goap_connect::Client::builder(&url);
        if !token.is_empty() {
            b = b.header(TOKEN_HEADER, &token);
        }
        Jobs {
            client: jobspb::JobServiceClient::new(b.build()),
        }
    }
}

/// A principal as the job service keeps it (`authz.Principal` in JSON, `jobkit.EncodePrincipal`).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct Principal {
    #[serde(skip_serializing_if = "String::is_empty")]
    subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    org: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    project: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    roles: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    global: Vec<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    project_admin: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    project_owner: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    on_behalf_of: String,
}

impl Principal {
    fn of(c: &Caller) -> Principal {
        Principal {
            subject: c.subject.clone(),
            org: c.org.clone(),
            project: c.project.clone(),
            roles: c.roles.clone(),
            on_behalf_of: c.on_behalf_of.clone(),
            ..Principal::default()
        }
    }

    /// The caller a run acts as: the principal of its job, as its headers would carry it.
    fn caller(&self) -> Caller {
        let mut roles = self.roles.clone();
        roles.extend(self.global.iter().cloned());
        Caller {
            subject: self.subject.clone(),
            on_behalf_of: if self.subject.starts_with("system:") {
                self.on_behalf_of.clone()
            } else {
                String::new()
            },
            at_least: false,
            project: self.project.clone(),
            command: String::new(),
            roles,
            org: self.org.clone(),
        }
    }
}

/// The person a job runs for: the caller, or the person a service acts for (`Principal.EndUser`).
fn end_user(c: &Caller) -> &str {
    if c.subject.starts_with("system:") {
        &c.on_behalf_of
    } else {
        &c.subject
    }
}

/// A job to create (`jobkit.Spec`).
pub(super) struct Spec {
    pub(super) kind: String,
    pub(super) label: String,
    pub(super) target: String,
    pub(super) params: serde_json::Map<String, Json>,
    pub(super) project_id: String,
    pub(super) owner_org: String,
    pub(super) input: Vec<u8>,
}

impl Graph {
    /// The job service and the person a job of `caller` runs for (Go's `jobs` and `caller`).
    pub(super) fn job_caller(&self, caller: &Caller) -> Result<Arc<Jobs>, ConnectError> {
        let Some(jobs) = self.state.jobs.clone() else {
            return Err(ConnectError::unimplemented(
                "no job service: this deployment runs no long job",
            ));
        };
        if end_user(caller).is_empty() {
            return Err(ConnectError::unauthenticated("a job runs for an identified person"));
        }
        Ok(jobs)
    }

    pub(super) async fn create_job(&self, jobs: &Jobs, caller: &Caller, s: Spec) -> Result<jobspb::Job, ConnectError> {
        let principal = Principal::of(caller);
        let req = jobspb::CreateJobRequest {
            kind: s.kind,
            label: s.label,
            target: s.target,
            params: (!s.params.is_empty()).then(|| convert::object(&s.params)),
            project_id: s.project_id,
            owner_org: s.owner_org,
            by: end_user(caller).into(),
            principal: serde_json::to_string(&principal).unwrap_or_default(),
            input: s.input,
        };
        let r = jobs.client.create_job(&Ctx::background(), Request::new(req)).await?;
        r.into_inner()
            .job
            .ok_or_else(|| ConnectError::internal("the job service answered no job"))
    }

    /// `StartGraphExport` (Go's `Handler.StartGraphExport`): the branch head or the baseline checked now, the state read
    /// and written as the result of the job by its run.
    pub(super) async fn start_graph_export_rpc(
        &self,
        caller: Caller,
        stamp: Stamp,
        m: pb::StartGraphExportRequest,
    ) -> Answer<pb::StartGraphExportResponse> {
        let jobs = self.job_caller(&caller)?;
        let format = if m.format.is_empty() {
            FORMAT_JSON
        } else {
            m.format.as_str()
        };
        if format != FORMAT_JSON && format != FORMAT_OKF {
            return Err(ConnectError::invalid_argument(format!(
                "format \"{format}\": json or okf"
            )));
        }
        let ns = namespace_of(&m.namespace).to_string();
        let at = {
            let tx = self
                .state
                .store
                .reader()
                .await
                .map_err(|e| connect_error(GraphError::internal(e)))?;
            if !m.baseline_id.is_empty() {
                let b = read::header_of(&tx, &BaselineId(m.baseline_id.clone()))
                    .await
                    .map_err(connect_error)?;
                if b.namespace != ns {
                    return Err(ConnectError::invalid_argument(format!(
                        "baseline {} is a baseline of {}, not {ns}",
                        m.baseline_id, b.namespace
                    )));
                }
                m.baseline_id.clone()
            } else {
                let branch = graph_core::model::branch_of(&m.branch);
                if tx
                    .branch(&ns, branch)
                    .await
                    .map_err(connect_error)?
                    .is_some_and(|b| b.ephemeral)
                {
                    return Err(connect_error(GraphError::unimplemented(
                        "the head of the branch of a change: answered by the Go service",
                    )));
                }
                read::branch_head(&tx, &ns, branch).await.map_err(connect_error)?;
                branch.to_string()
            }
        };
        let label = format!(
            "Export {ns} at {at} ({})",
            if format == FORMAT_OKF { "OKF" } else { "JSON" }
        );
        let mut params = serde_json::Map::new();
        params.insert("namespace".into(), ns.clone().into());
        params.insert("branch".into(), m.branch.clone().into());
        params.insert("baselineId".into(), m.baseline_id.clone().into());
        params.insert("format".into(), format.into());
        let spec = Spec {
            kind: KIND_EXPORT.into(),
            label,
            target: ns,
            params,
            project_id: caller.project.clone(),
            owner_org: String::new(),
            input: Vec::new(),
        };
        let job = self.create_job(&jobs, &caller, spec).await?;
        stamp.on(Ok(Response::new(pb::StartGraphExportResponse { job: Some(job) })))
    }

    /// `StartGraphImport` (Go's `Handler.StartGraphImport`): the snapshot read now, imported by the run of the job.
    pub(super) async fn start_graph_import_rpc(
        &self,
        caller: Caller,
        stamp: Stamp,
        m: pb::StartGraphImportRequest,
    ) -> Answer<pb::StartGraphImportResponse> {
        let jobs = self.job_caller(&caller)?;
        let Some(parsed) = graph_core::snapshot::parse_snapshot(&m.document) else {
            return Err(connect_error(GraphError::unimplemented(
                "a snapshot the decoder refuses, in Go's words: answered by the Go service",
            )));
        };
        let s = parsed.map_err(connect_error)?;
        let (owner, owner_stamp) = match self.resolve_owner(&caller, &m.owner_org).await {
            Ok(v) => v,
            Err((e, st)) => return stamp.or(st.0).on(Err(e)),
        };
        let stamp = stamp.or(owner_stamp.0);
        let project = if !m.project_id.is_empty() {
            m.project_id.clone()
        } else if !caller.project.is_empty() {
            caller.project.clone()
        } else {
            let tx = self
                .state
                .store
                .reader()
                .await
                .map_err(|e| connect_error(GraphError::internal(e)))?;
            let catalog = self.state.catalog(&tx).await.map_err(connect_error)?;
            catalog
                .structure(STRUCTURE_PROJECT)
                .map(|s| s.root.clone())
                .unwrap_or_default()
        };
        let mut params = serde_json::Map::new();
        params.insert("title".into(), m.title.clone().into());
        params.insert("intent".into(), m.intent.clone().into());
        params.insert("methodology".into(), m.methodology.clone().into());
        let spec = Spec {
            kind: KIND_IMPORT.into(),
            label: format!("Import a snapshot of {} ({} nodes)", s.namespace, s.nodes.len()),
            target: s.namespace.clone(),
            params,
            project_id: project,
            owner_org: owner,
            input: m.document.into_bytes(),
        };
        let res = self.create_job(&jobs, &caller, spec).await;
        stamp.on(res.map(|job| Response::new(pb::StartGraphImportResponse { job: Some(job) })))
    }
}

/// What a running job says of itself, sent by its reporter.
#[derive(Default)]
struct Progress {
    progress: String,
    done: i64,
    total: i64,
    stats: BTreeMap<String, i64>,
    output: serde_json::Map<String, Json>,
    lines: Vec<jobspb::LogLine>,
    dirty: bool,
}

/// A job as its runner sees it (`jobkit.Job`).
pub struct Job {
    id: String,
    worker: String,
    params: serde_json::Map<String, Json>,
    project_id: String,
    owner_org: String,
    principal: Principal,
    jobs: Arc<Jobs>,
    state: StdMutex<Progress>,
}

impl Job {
    fn string(&self, name: &str) -> String {
        match self.params.get(name) {
            Some(Json::String(s)) => s.clone(),
            None | Some(Json::Null) => String::new(),
            Some(v) => v.to_string(),
        }
    }

    fn progress(&self, line: impl Into<String>) {
        let line = line.into();
        let mut p = self.state.lock().unwrap();
        if !line.is_empty() {
            p.lines.push(jobspb::LogLine {
                at: convert::time(&Some(chrono::Utc::now())),
                text: line.clone(),
            });
            if p.lines.len() > MAX_LOG {
                let cut = p.lines.len() - MAX_LOG;
                p.lines.drain(..cut);
            }
        }
        p.progress = line;
        p.dirty = true;
    }

    fn step(&self, done: i64, total: i64) {
        let mut p = self.state.lock().unwrap();
        (p.done, p.total, p.dirty) = (done, total, true);
    }

    fn set(&self, name: &str, n: i64) {
        let mut p = self.state.lock().unwrap();
        p.stats.insert(name.into(), n);
        p.dirty = true;
    }

    fn output(&self, kv: serde_json::Map<String, Json>) {
        let mut p = self.state.lock().unwrap();
        p.output.extend(kv);
        p.dirty = true;
    }

    /// What the job says now, and whether it changed since the last call (`Job.report`).
    fn report(&self) -> (jobspb::ReportJobRequest, bool) {
        let mut p = self.state.lock().unwrap();
        let dirty = std::mem::take(&mut p.dirty);
        let r = jobspb::ReportJobRequest {
            id: self.id.clone(),
            worker: self.worker.clone(),
            progress: p.progress.clone(),
            done: p.done,
            total: p.total,
            stats: p.stats.iter().map(|(k, v)| (k.clone(), *v)).collect(),
            output: (!p.output.is_empty()).then(|| convert::object(&p.output)),
            log: std::mem::take(&mut p.lines),
            ..Default::default()
        };
        (r, dirty)
    }

    /// Takes back the lines of a report that did not reach the service (`Job.unreport`).
    fn unreport(&self, r: jobspb::ReportJobRequest) {
        if r.log.is_empty() {
            return;
        }
        let mut p = self.state.lock().unwrap();
        let mut lines = r.log;
        lines.append(&mut p.lines);
        if lines.len() > MAX_LOG {
            let cut = lines.len() - MAX_LOG;
            lines.drain(..cut);
        }
        p.lines = lines;
        p.dirty = true;
    }

    /// The whole input of the job.
    async fn input(&self) -> Result<Vec<u8>, ConnectError> {
        let mut buf = Vec::new();
        loop {
            let r = self
                .jobs
                .client
                .read_job_input(
                    &Ctx::background(),
                    Request::new(jobspb::ReadJobInputRequest {
                        id: self.id.clone(),
                        worker: self.worker.clone(),
                        offset: buf.len() as i64,
                    }),
                )
                .await?
                .into_inner();
            let n = r.data.len();
            buf.extend_from_slice(&r.data);
            if r.eof || n == 0 {
                return Ok(buf);
            }
        }
    }

    /// Writes the file of the job, in chunks (`Job.Result`).
    async fn result(&self, name: &str, media_type: &str, data: &[u8]) -> Result<(), ConnectError> {
        let mut chunks: Vec<&[u8]> = data.chunks(RESULT_CHUNK).collect();
        if chunks.is_empty() {
            chunks.push(&[]);
        }
        for c in chunks {
            self.jobs
                .client
                .write_job_result(
                    &Ctx::background(),
                    Request::new(jobspb::WriteJobResultRequest {
                        id: self.id.clone(),
                        worker: self.worker.clone(),
                        name: name.into(),
                        media_type: media_type.into(),
                        data: c.to_vec(),
                    }),
                )
                .await?;
        }
        Ok(())
    }
}

/// The error a run ends with: its message, as Go's `err.Error()`.
fn failure(e: ConnectError) -> String {
    e.message().to_string()
}

/// The failure of an import as Go's job records it: the gates of the import answer connect errors, which a job
/// records with their code.
fn gate_failure(e: ConnectError) -> String {
    if e.code() == goap_connect::Code::PermissionDenied {
        format!("permission_denied: {}", e.message())
    } else {
        e.message().to_string()
    }
}

impl Graph {
    /// The kinds the worker of this process claims (`Handler.JobRunners`).
    fn job_kinds() -> Vec<String> {
        vec![KIND_EXPORT.into(), KIND_IMPORT.into()]
    }

    /// Claims and runs the jobs of the graph's kinds as long as the process (`jobkit.Worker.Run`): two at a time, a claim
    /// waiting 20 s, retried every 2 s while the job service cannot be reached.
    pub async fn run_worker(self: Arc<Self>) {
        let Some(jobs) = self.state.jobs.clone() else { return };
        let worker = worker_id("graph");
        let kinds = Self::job_kinds();
        let slots = Arc::new(tokio::sync::Semaphore::new(2));
        let mut failing = false;
        loop {
            let Ok(slot) = slots.clone().acquire_owned().await else {
                return;
            };
            let claim = jobs
                .client
                .claim_job(
                    &Ctx::background(),
                    Request::new(jobspb::ClaimJobRequest {
                        worker: worker.clone(),
                        kinds: kinds.clone(),
                        wait_ms: 20_000,
                    }),
                )
                .await;
            let c = match claim {
                Ok(c) => c.into_inner(),
                Err(e) => {
                    drop(slot);
                    if !failing {
                        tracing::warn!(worker, error = %e.message(), "job claim failed, retrying");
                        failing = true;
                    }
                    tokio::time::sleep(Duration::from_secs(2)).await;
                    continue;
                }
            };
            if failing {
                tracing::info!(worker, ?kinds, "job service reached");
                failing = false;
            }
            let Some(pbjob) = c.job else { continue };
            let lease = if c.lease_seconds > 0 {
                Duration::from_secs(c.lease_seconds as u64)
            } else {
                DEFAULT_LEASE
            };
            let job = Arc::new(Job {
                id: pbjob.id.clone(),
                worker: worker.clone(),
                params: pbjob.params.as_ref().map(convert::map).unwrap_or_default(),
                project_id: pbjob.project_id.clone(),
                owner_org: pbjob.owner_org.clone(),
                principal: serde_json::from_str(&c.principal).unwrap_or_default(),
                jobs: jobs.clone(),
                state: StdMutex::new(Progress::default()),
            });
            let g = self.clone();
            let kind = pbjob.kind.clone();
            tokio::spawn(async move {
                g.run_job(&kind, job, lease).await;
                drop(slot);
            });
        }
    }

    /// Runs a claimed job (`Worker.run`): a reporter sends its progress and renews its lease while the runner works, a
    /// cancel asked of it drops the run.
    async fn run_job(&self, kind: &str, job: Arc<Job>, lease: Duration) {
        tracing::info!(job = %job.id, kind, by = %job.principal.subject, "job started");
        let cancel = Arc::new(tokio::sync::Notify::new());
        let reporter = {
            let job = job.clone();
            let cancel = cancel.clone();
            tokio::spawn(async move {
                let mut tick = tokio::time::interval(REPORT_EVERY);
                tick.tick().await;
                let mut last = std::time::Instant::now();
                loop {
                    tick.tick().await;
                    let (r, dirty) = job.report();
                    if !dirty && last.elapsed() < HEARTBEAT.min(lease / 3) {
                        continue;
                    }
                    match job
                        .jobs
                        .client
                        .report_job(&Ctx::background(), Request::new(r.clone()))
                        .await
                    {
                        Ok(res) => {
                            last = std::time::Instant::now();
                            if res.into_inner().cancel {
                                cancel.notify_one();
                            }
                        }
                        Err(e) => {
                            tracing::warn!(job = %job.id, error = %e.message(), "job report failed");
                            if e.code() == goap_connect::Code::FailedPrecondition {
                                cancel.notify_one(); // the job is no longer this worker's
                            } else {
                                job.unreport(r);
                            }
                        }
                    }
                }
            })
        };
        let run = async {
            match kind {
                KIND_EXPORT => self.run_export(&job).await,
                KIND_IMPORT => self.run_import(&job).await,
                _ => Err(format!("no runner of kind {kind}")),
            }
        };
        let (status, error) = tokio::select! {
            r = run => match r {
                Ok(()) => ("done", String::new()),
                Err(e) => ("failed", e),
            },
            () = cancel.notified() => ("cancelled", "context canceled".to_string()),
        };
        reporter.abort();
        let (mut r, _) = job.report();
        r.status = status.into();
        r.error = error.clone();
        if let Err(e) = job.jobs.client.report_job(&Ctx::background(), Request::new(r)).await {
            tracing::warn!(job = %job.id, status, error = %e.message(), "job end not recorded");
        }
        if error.is_empty() {
            tracing::info!(job = %job.id, "job done");
        } else {
            tracing::warn!(job = %job.id, status, error, "job ended");
        }
    }

    /// The run of an export (`Handler.runExport`).
    async fn run_export(&self, j: &Job) -> Result<(), String> {
        j.progress("reading the state");
        let (ns, branch, baseline) = (j.string("namespace"), j.string("branch"), j.string("baselineId"));
        let caller = j.principal.caller();
        let s = self
            .read_changes(caller, &[], async |tx, who| {
                let reach = who.reach();
                graph_core::snapshot::export_snapshot(
                    tx,
                    &ns,
                    &branch,
                    &BaselineId(baseline.clone()),
                    reach.as_ref(),
                    chrono::Utc::now(),
                )
                .await
            })
            .await
            .map_err(failure)?
            .into_inner();
        let total = s.nodes.len() as i64;
        j.set("nodes", total);
        let stamp = s.exported_at.format("%Y%m%d-%H%M%S");
        if j.string("format") == FORMAT_OKF {
            let catalog = self.catalog().await.map_err(|e| e.message().to_string())?;
            let options = graph_core::okf::Options {
                catalog: Some(&catalog),
            };
            j.progress("writing the OKF tree");
            let zip = graph_core::okf::write(&s, &options, s.exported_at, |done, _| {
                if done % 50 == 0 {
                    j.step(done as i64, total);
                }
            })?;
            j.result(
                &format!("graph-{}-{stamp}.okf.zip", s.namespace),
                "application/zip",
                &zip,
            )
            .await
            .map_err(failure)?;
        } else {
            let doc = graph_core::gojson::to_string_indent(&s, "  ");
            j.result(
                &format!("graph-{}-{stamp}.json", s.namespace),
                "application/json",
                doc.as_bytes(),
            )
            .await
            .map_err(failure)?;
        }
        j.step(total, total);
        j.progress(format!("{total} node(s) exported"));
        let mut out = serde_json::Map::new();
        out.insert("nodes".into(), total.into());
        out.insert("baselineId".into(), s.baseline.clone().into());
        out.insert("branch".into(), s.branch.clone().into());
        j.output(out);
        Ok(())
    }

    /// The run of a snapshot import (`Handler.runImport`).
    async fn run_import(&self, j: &Job) -> Result<(), String> {
        let doc = j.input().await.map_err(failure)?;
        let doc = String::from_utf8_lossy(&doc).into_owned();
        let s = match graph_core::snapshot::parse_snapshot(&doc) {
            Some(Ok(s)) => s,
            Some(Err(e)) => return Err(e.message),
            None => return Err("a snapshot the decoder refuses".into()),
        };
        j.progress(format!("importing {} node(s) into {}", s.nodes.len(), s.namespace));
        let req = pb::ImportGraphRequest {
            document: doc,
            title: j.string("title"),
            intent: j.string("intent"),
            methodology: j.string("methodology"),
            owner_org: j.owner_org.clone(),
            project_id: j.project_id.clone(),
        };
        let out = self
            .import_graph_rpc(j.principal.caller(), req)
            .await
            .map_err(gate_failure)?
            .into_inner();
        j.set("created", out.created.into());
        j.set("updated", out.updated.into());
        j.set("unchanged", out.unchanged.into());
        let mut res = serde_json::Map::new();
        res.insert("created".into(), out.created.into());
        res.insert("updated".into(), out.updated.into());
        res.insert("unchanged".into(), out.unchanged.into());
        res.insert("warnings".into(), out.warnings.clone().into());
        match &out.change {
            Some(c) => {
                res.insert("changeId".into(), c.id.clone().into());
                res.insert("changeTitle".into(), c.title.clone().into());
                j.progress(format!("change created: {}", c.id));
            }
            None => j.progress("the graph already holds this snapshot: nothing to import"),
        }
        j.output(res);
        Ok(())
    }
}

/// A worker id for this process: `<name>@<host>:<pid>` (`jobkit.WorkerID`).
fn worker_id(name: &str) -> String {
    let host = std::fs::read_to_string("/proc/sys/kernel/hostname")
        .ok()
        .or_else(|| std::env::var("HOSTNAME").ok())
        .unwrap_or_default();
    format!("{name}@{}:{}", host.trim(), std::process::id())
}
