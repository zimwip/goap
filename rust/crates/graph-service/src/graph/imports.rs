//! The import registry (Go's `graph/importsvc`, ADR 0148): the importers the domains in force declare, each available
//! while a live worker of the job service claims its kind; a run, or the listing of a folder chooser, is a job created
//! for the person who asked, once the permission import:run and the parameters are checked.
use std::time::{Duration, Instant};

use axum::http::{HeaderMap, HeaderValue};

use super::jobs::Spec;
use super::*;
use crate::Imports;
use goap_proto::importer::v1 as ipb;
use goap_proto::jobs::v1 as jobspb;
use goap_proto::jobs::v1::JobService as _;
use goap_proto::registry::v1 as rpb;
use graph_catalog::{catalog::AttributeCheck, def};
use graph_core::write::check::check_value;
use serde::Deserialize;
use serde_json::{Map, Value as Json, json};

type Answer<T> = Result<Response<T>, ConnectError>;

/// The bound of the wait of a listing, and how often its job is read meanwhile (`browseWait`, `browsePoll`).
const BROWSE_WAIT: Duration = Duration::from_secs(15);
const BROWSE_POLL: Duration = Duration::from_millis(50);

/// The widget of a parameter naming a directory (`def.WidgetDirectory`).
const WIDGET_DIRECTORY: &str = "directory";

/// An importer as a domain declares it (`def.Importer`).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
struct Decl {
    name: String,
    label: String,
    params: Vec<def::Attribute>,
}

/// The kind of the runs of an importer (`importerkit.Kind`) and of its chooser (`BrowseKind`).
fn kind(domain: &str, importer: &str) -> String {
    format!("import.{domain}.{importer}")
}

fn browse_kind(domain: &str, importer: &str) -> String {
    format!("browse.{domain}.{importer}")
}

/// The headers naming the caller on a call made for it (`identity.SetHeaders`): the job service shows a job to whom
/// it runs for.
fn identity(c: &Caller) -> HeaderMap {
    let mut h = HeaderMap::new();
    let mut set = |k: &'static str, v: &str| {
        if let Ok(v) = HeaderValue::from_str(v) {
            h.insert(k, v);
        }
    };
    set(HEADER_SUBJECT, &c.subject);
    set(HEADER_ORG, &c.org);
    set(HEADER_PROJECT, &c.project);
    set(HEADER_ROLES, &c.roles.join(","));
    if c.subject.starts_with("system:") && !c.on_behalf_of.is_empty() {
        set(HEADER_ON_BEHALF_OF, &c.on_behalf_of);
    }
    h
}

fn internal(e: impl std::fmt::Display) -> ConnectError {
    connect_error(GraphError::internal(e.to_string()))
}

impl Imports {
    fn graph(&self) -> Graph {
        Graph {
            state: self.state.clone(),
        }
    }

    /// The latest published version of each domain (`domainsvc.Service.Domains`).
    async fn domains(&self) -> Result<Vec<def::Domain>, ConnectError> {
        self.state.domains_in_force().await.map_err(connect_error)
    }

    /// The importer `d/i` the domains in force declare, with its domain.
    async fn declared(&self, d: &str, i: &str) -> Result<(def::Domain, Decl, Json), ConnectError> {
        for dom in self.domains().await? {
            if dom.name != d {
                continue;
            }
            for v in &dom.importers {
                let decl: Decl = serde_json::from_value(v.clone()).map_err(internal)?;
                if decl.name == i {
                    let v = v.clone();
                    return Ok((dom, decl, v));
                }
            }
        }
        Err(ConnectError::not_found(format!(
            "importer {d}/{i}: declared by no domain in force: not found"
        )))
    }

    /// The kinds a live worker claims, with the first worker claiming each (`jobkit.LiveKinds`); none without a job
    /// service.
    async fn serving(&self) -> Result<HashMap<String, String>, ConnectError> {
        let mut out = HashMap::new();
        let Some(jobs) = &self.state.jobs else {
            return Ok(out);
        };
        let r = jobs
            .client
            .list_workers(&Ctx::background(), Request::new(jobspb::ListWorkersRequest {}))
            .await?;
        for w in r.into_inner().workers {
            for k in w.kinds {
                out.entry(k).or_insert_with(|| w.id.clone());
            }
        }
        Ok(out)
    }

    /// import:run of `d/i` on the project for the caller (`authz.Check`).
    async fn authorize(&self, c: &Caller, d: &str, i: &str, project: &str) -> Result<(), ConnectError> {
        let tx = self.state.reader().await.map_err(connect_error)?;
        let snap = access::Snapshot::read(&tx).await.map_err(connect_error)?;
        let res = abac::Resource {
            typ: "import".into(),
            namespace: d.into(),
            name: i.into(),
            project_id: project.into(),
            ..abac::Resource::default()
        };
        if snap.authorize(&c.reader_or_self(), res, "run").map_err(connect_error)? {
            return Ok(());
        }
        Err(connect_error(forbidden(&c.subject, "run", "import", "")))
    }
}

/// The values of a run checked against the parameters an importer declares, with the defaults (`importsvc.Params`).
fn params(d: &def::Domain, im: &Decl, values: &Map<String, Json>) -> Result<Map<String, Json>, ConnectError> {
    let mut out = Map::new();
    for a in &im.params {
        if !a.default.is_empty() {
            out.insert(a.name.clone(), Json::String(a.default.clone()));
        }
    }
    let mut problems = Vec::new();
    for (k, v) in values {
        let Some(a) = im.params.iter().find(|a| &a.name == k) else {
            problems.push(format!("{k}: not a parameter of {}", im.name));
            continue;
        };
        if v.is_null() || v.as_str() == Some("") {
            continue;
        }
        let check = AttributeCheck {
            name: a.name.clone(),
            typ: a.typ.clone(),
            enumeration: a.enumeration.clone(),
            values: d
                .enum_values(&a.enumeration)
                .map(|vs| vs.iter().map(|v| v.value.clone()).collect())
                .unwrap_or_default(),
        };
        if let Err(e) = check_value(&check, Some(v)) {
            problems.push(format!("{k}: {e}"));
            continue;
        }
        out.insert(k.clone(), v.clone());
    }
    if !problems.is_empty() {
        problems.sort();
        return Err(ConnectError::invalid_argument(format!(
            "{}: invalid",
            problems.join("; ")
        )));
    }
    Ok(out)
}

#[async_trait]
impl ipb::ImportService for Imports {
    async fn list_importers(
        &self,
        _ctx: &Ctx,
        req: Request<ipb::ListImportersRequest>,
    ) -> Answer<ipb::ListImportersResponse> {
        let ns = req.into_inner().namespace;
        let ds = self.domains().await?;
        let live = self.serving().await?;
        let mut out = Vec::new();
        for d in &ds {
            if !ns.is_empty() && d.name != ns {
                continue;
            }
            let decls =
                crate::domainpb::domain(&json!({"name": d.name, "importers": d.importers})).map_err(internal)?;
            for (v, p) in d.importers.iter().zip(decls.importers) {
                let decl: Decl = serde_json::from_value(v.clone()).map_err(internal)?;
                let service = live.get(&kind(&d.name, &decl.name)).cloned().unwrap_or_default();
                out.push(ipb::Importer {
                    domain: d.name.clone(),
                    domain_version: d.version.clone(),
                    name: p.name,
                    label: p.label,
                    description: p.description,
                    params: p
                        .params
                        .into_iter()
                        .zip(&decl.params)
                        .map(|(a, x)| rpb::AttributeInfo {
                            attribute: Some(a),
                            values: d
                                .enum_values(&x.enumeration)
                                .unwrap_or_default()
                                .iter()
                                .map(|v| rpb::EnumValue {
                                    value: v.value.clone(),
                                    label: v.label.clone(),
                                })
                                .collect(),
                            ..rpb::AttributeInfo::default()
                        })
                        .collect(),
                    available: !service.is_empty(),
                    service,
                });
            }
        }
        out.sort_by(|a, b| a.domain.cmp(&b.domain).then_with(|| a.name.cmp(&b.name)));
        Ok(Response::new(ipb::ListImportersResponse { importers: out }))
    }

    async fn start_import(
        &self,
        _ctx: &Ctx,
        req: Request<ipb::StartImportRequest>,
    ) -> Answer<ipb::StartImportResponse> {
        let c = Caller::headers(&req);
        let m = req.into_inner();
        if c.subject.is_empty() {
            return Err(ConnectError::permission_denied(
                "an import acts for a person: permission denied",
            ));
        }
        let project = if m.project_id.is_empty() {
            c.project.clone()
        } else {
            m.project_id.clone()
        };
        if project.is_empty() {
            return Err(ConnectError::invalid_argument(
                "project required (the changes of an import act in a project): invalid",
            ));
        }
        let (d, decl, _) = self.declared(&m.domain, &m.importer).await?;
        self.authorize(&c, &m.domain, &m.importer, &project).await?;
        let values = m.params.as_ref().map(convert::map).unwrap_or_default();
        let params = params(&d, &decl, &values)?;
        let kind = kind(&m.domain, &m.importer);
        if !self.serving().await?.contains_key(&kind) {
            return Err(ConnectError::unavailable(format!(
                "importer {}/{}: no service implements it now: unavailable",
                m.domain, m.importer
            )));
        }
        let g = self.graph();
        let jobs = g.job_caller(&c)?;
        let label = if decl.label.is_empty() {
            decl.name.clone()
        } else {
            decl.label.clone()
        };
        let spec = Spec {
            kind,
            label,
            target: m.domain.clone(),
            params,
            project_id: project,
            owner_org: m.owner_org,
            input: Vec::new(),
        };
        let job = g.create_job(&jobs, &c, spec).await?;
        Ok(Response::new(ipb::StartImportResponse { job: Some(job) }))
    }

    async fn browse_import(
        &self,
        _ctx: &Ctx,
        req: Request<ipb::BrowseImportRequest>,
    ) -> Answer<ipb::BrowseImportResponse> {
        let c = Caller::headers(&req);
        let m = req.into_inner();
        if c.subject.is_empty() {
            return Err(ConnectError::permission_denied(
                "a chooser acts for a person: permission denied",
            ));
        }
        let project = if m.project_id.is_empty() {
            c.project.clone()
        } else {
            m.project_id.clone()
        };
        let (_, decl, _) = self.declared(&m.domain, &m.importer).await?;
        if !decl
            .params
            .iter()
            .any(|a| a.name == m.param && a.widget == WIDGET_DIRECTORY)
        {
            return Err(ConnectError::invalid_argument(format!(
                "{}: not a directory parameter of {}: invalid",
                m.param, m.importer
            )));
        }
        self.authorize(&c, &m.domain, &m.importer, &project).await?;
        let kind = browse_kind(&m.domain, &m.importer);
        if !self.serving().await?.contains_key(&kind) {
            return Err(ConnectError::unavailable(format!(
                "importer {}/{}: no service lists its directories now: unavailable",
                m.domain, m.importer
            )));
        }
        let g = self.graph();
        let jobs = g.job_caller(&c)?;
        let mut ps = Map::new();
        ps.insert("param".into(), Json::String(m.param.clone()));
        ps.insert("path".into(), Json::String(m.path.clone()));
        let spec = Spec {
            kind,
            label: "Browse".into(),
            target: m.domain.clone(),
            params: ps,
            project_id: project,
            owner_org: String::new(),
            input: Vec::new(),
        };
        let mut job = g.create_job(&jobs, &c, spec).await?;
        let until = Instant::now() + BROWSE_WAIT;
        while !matches!(job.status.as_str(), "done" | "failed" | "cancelled") {
            if Instant::now() >= until {
                return Err(ConnectError::unavailable(format!(
                    "importer {}/{}: no listing in 15s: unavailable",
                    m.domain, m.importer
                )));
            }
            tokio::time::sleep(BROWSE_POLL).await;
            job = jobs
                .client
                .get_job(
                    &Ctx::background(),
                    Request::with_headers(jobspb::GetJobRequest { id: job.id.clone() }, identity(&c)),
                )
                .await?
                .into_inner()
                .job
                .ok_or_else(|| internal("the job service answered no job"))?;
        }
        if job.status != "done" {
            return Err(ConnectError::invalid_argument(format!("{}: invalid", job.error)));
        }
        let output = job.output.as_ref().map(convert::map).unwrap_or_default();
        let Some(listing) = output.get("listing") else {
            return Err(internal("the job answered no listing"));
        };
        let l: ipb::BrowseImportResponse = serde_json::from_value(listing.clone()).map_err(internal)?;
        Ok(Response::new(l))
    }
}
