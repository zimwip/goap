//! The Connect `registry.v1.RegistryService`.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_connect::{Code, ConnectError, Ctx, Request, Response, async_trait};
use goap_def::{Issue, Issues};
use goap_domain::{Blackboard, methodology_ref};
use goap_domainpb as domainpb;
use goap_gen::registry::v1::{self as pb, RegistryService};
use goap_identity::Extractor;
use goap_methodology::Compiled;

use crate::convert::{
    from_pb, levels_to_pb, methodology_view_to_pb, plan_preview_to_pb, process_graph_to_pb,
    summary_to_pb, to_pb,
};
use crate::error::Error;
use crate::service::Service;

/// Handler implements the Connect `RegistryService` (`goap_gen::registry::v1::registry_service_router`).
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    /// Identity extracts the caller from request headers.
    pub identity: Extractor,
}

impl Handler {
    pub fn new(service: Arc<Service>) -> Handler {
        Handler {
            service,
            identity: Extractor::default(),
        }
    }

    /// compiledFor is the methodology a read of its shape is about: the one given as edited, compiled leniently with
    /// its issues (a draft is drawn as far as it stands), else the stored version name / version names, compiled
    /// against its domains (ADR 0113; the version a change follows).
    async fn compiled_for(
        &self,
        ctx: &Ctx,
        edited: Option<&pb::Methodology>,
        name: &str,
        version: &str,
    ) -> Result<(Option<Arc<Compiled>>, Issues), ConnectError> {
        if edited.is_some() || name.is_empty() {
            let (c, issues) = from_pb(edited).compile_lenient();
            return Ok((c.map(Arc::new), issues));
        }
        match self
            .service
            .methodology(ctx, &methodology_ref(name, version))
            .await
        {
            Ok(c) => Ok((Some(c), Issues::default())),
            Err(e) if e.is_suspended() => {
                Err(ConnectError::from_error(Code::FailedPrecondition, e))
            }
            Err(e) => Err(ConnectError::from_error(Code::NotFound, e)),
        }
    }
}

/// toConnect maps the errors of the service to Connect codes.
pub fn to_connect(err: Error) -> ConnectError {
    let code = if err.is_not_found() {
        Code::NotFound
    } else if err.is_immutable() {
        Code::FailedPrecondition
    } else if err.is_invalid() {
        Code::InvalidArgument
    } else if err.is_forbidden() {
        Code::PermissionDenied
    } else {
        Code::Internal
    };
    ConnectError::from_error(code, err)
}

#[async_trait]
impl RegistryService for Handler {
    async fn list_methodologies(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListMethodologiesRequest>,
    ) -> Result<Response<pb::ListMethodologiesResponse>, ConnectError> {
        let rs = self
            .service
            .versions(ctx, r.msg.all_versions)
            .await
            .map_err(to_connect)?;
        let mut out = pb::ListMethodologiesResponse::default();
        for rec in &rs {
            let mut sum = summary_to_pb(rec);
            sum.suspended = self.service.suspended(ctx, rec).await;
            out.methodologies.push(sum);
        }
        Ok(Response::new(out))
    }

    async fn get_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetMethodologyRequest>,
    ) -> Result<Response<pb::GetMethodologyResponse>, ConnectError> {
        let rec = self
            .service
            .get(ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        let mut m = to_pb(&rec);
        m.suspended = self.service.suspended(ctx, &rec).await;
        let mut out = pb::GetMethodologyResponse {
            methodology: Some(m),
            domains: Vec::new(),
        };
        if r.msg.with_domains {
            let ds = self
                .service
                .compile_domains(ctx, &rec)
                .await
                .map_err(to_connect)?;
            for d in ds {
                out.domains.push(domainpb::domain_to_pb(&goap_def::Record {
                    domain: d,
                    status: goap_def::STATUS_PUBLISHED,
                    ..goap_def::Record::default()
                }));
            }
        }
        Ok(Response::new(out))
    }

    async fn save_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::SaveMethodologyRequest>,
    ) -> Result<Response<pb::SaveMethodologyResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let (rec, issues) = self
            .service
            .save(&ctx, from_pb(r.msg.methodology.as_ref()))
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::SaveMethodologyResponse {
            methodology: Some(to_pb(&rec)),
            issues: domainpb::issues_to_pb(&issues),
        }))
    }

    async fn validate_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::ValidateMethodologyRequest>,
    ) -> Result<Response<pb::ValidateMethodologyResponse>, ConnectError> {
        let m = from_pb(r.msg.methodology.as_ref());
        Ok(Response::new(pb::ValidateMethodologyResponse {
            issues: domainpb::issues_to_pb(&self.service.validate(ctx, &m).await),
        }))
    }

    /// GetProcessGraph builds the graph of a process of a methodology as edited (ADR 0036 §4), with the issues of
    /// the methodology: a draft that does not compile still has its graph drawn as far as it stands.
    async fn get_process_graph(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetProcessGraphRequest>,
    ) -> Result<Response<pb::GetProcessGraphResponse>, ConnectError> {
        let (c, issues) = self
            .compiled_for(ctx, r.msg.methodology.as_ref(), &r.msg.name, &r.msg.version)
            .await?;
        let issues_pb = domainpb::issues_to_pb(&issues);
        let Some(c) = c else {
            return Ok(Response::new(pb::GetProcessGraphResponse {
                graph: None,
                issues: issues_pb,
            }));
        };
        match c.process_graph(&r.msg.process) {
            Some(g) => Ok(Response::new(pb::GetProcessGraphResponse {
                graph: Some(process_graph_to_pb(&g)),
                issues: issues_pb,
            })),
            None if issues.has_errors() => Ok(Response::new(pb::GetProcessGraphResponse {
                graph: None,
                issues: issues_pb,
            })),
            None => Err(ConnectError::not_found(format!(
                "no process {:?} in {}",
                r.msg.process,
                c.name()
            ))),
        }
    }

    /// GetMethodologyView is the compiled view of the activities of a methodology (ADR 0126 step 3): as edited, with
    /// its issues, drawn as far as it stands; else the stored version named.
    async fn get_methodology_view(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetMethodologyViewRequest>,
    ) -> Result<Response<pb::GetMethodologyViewResponse>, ConnectError> {
        let (c, issues) = self
            .compiled_for(ctx, r.msg.methodology.as_ref(), &r.msg.name, &r.msg.version)
            .await?;
        Ok(Response::new(pb::GetMethodologyViewResponse {
            view: c.map(|c| methodology_view_to_pb(&c.view())),
            issues: domainpb::issues_to_pb(&issues),
        }))
    }

    /// CheckLevels checks the coherence of a process or method, level by level, as edited, with the issues of the
    /// methodology.
    async fn check_levels(
        &self,
        ctx: &Ctx,
        r: Request<pb::CheckLevelsRequest>,
    ) -> Result<Response<pb::CheckLevelsResponse>, ConnectError> {
        let (c, issues) = self
            .compiled_for(ctx, r.msg.methodology.as_ref(), &r.msg.name, &r.msg.version)
            .await?;
        let issues_pb = domainpb::issues_to_pb(&issues);
        let Some(c) = c else {
            return Ok(Response::new(pb::CheckLevelsResponse {
                issues: issues_pb,
                ..pb::CheckLevelsResponse::default()
            }));
        };
        match c.check_levels(&r.msg.root) {
            Some(ls) => Ok(Response::new(pb::CheckLevelsResponse {
                levels: levels_to_pb(&ls),
                conditions: c.conditions.exprs().into_iter().collect(),
                issues: issues_pb,
            })),
            None if issues.has_errors() => Ok(Response::new(pb::CheckLevelsResponse {
                issues: issues_pb,
                ..pb::CheckLevelsResponse::default()
            })),
            None => Err(ConnectError::not_found(format!(
                "no process or method with steps {:?} in {}",
                r.msg.root,
                c.name()
            ))),
        }
    }

    /// PreviewPlan plans toward a goal with the planner its agent is actually configured with, from an empty
    /// blackboard whose evaluated conditions the request's overrides patch on top (ADR 0034; no live change needed).
    /// Like GetProcessGraph it plans over what stands of a draft and returns the issues of the methodology alongside
    /// the plan.
    async fn preview_plan(
        &self,
        _ctx: &Ctx,
        r: Request<pb::PreviewPlanRequest>,
    ) -> Result<Response<pb::PreviewPlanResponse>, ConnectError> {
        let (c, issues) = from_pb(r.msg.methodology.as_ref()).compile_lenient();
        let issues_pb = domainpb::issues_to_pb(&issues);
        let Some(c) = c else {
            return Ok(Response::new(pb::PreviewPlanResponse {
                preview: None,
                issues: issues_pb,
            }));
        };
        let overrides: BTreeMap<String, bool> = r
            .msg
            .overrides
            .iter()
            .map(|(k, v)| (k.clone(), *v))
            .collect();
        match goap_engine::preview_plan(
            &c,
            &Blackboard::default(),
            &r.msg.agent,
            &r.msg.goal,
            &overrides,
        ) {
            Ok(p) => Ok(Response::new(pb::PreviewPlanResponse {
                preview: Some(plan_preview_to_pb(&p)),
                issues: issues_pb,
            })),
            // the agent or goal may be one the draft's issues dropped: tell the issues
            Err(_) if issues.has_errors() => Ok(Response::new(pb::PreviewPlanResponse {
                preview: None,
                issues: issues_pb,
            })),
            Err(e) if e.is_live_planner() => Ok(Response::new(pb::PreviewPlanResponse {
                preview: None,
                issues: domainpb::issues_to_pb(&Issues(vec![Issue {
                    message: e.to_string(),
                    ..Issue::default()
                }])),
            })),
            Err(e) => Err(ConnectError::from_error(Code::NotFound, e)),
        }
    }

    async fn publish_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::PublishMethodologyRequest>,
    ) -> Result<Response<pb::PublishMethodologyResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let rec = self
            .service
            .publish_version(&ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::PublishMethodologyResponse {
            methodology: Some(to_pb(&rec)),
        }))
    }

    async fn create_version(
        &self,
        ctx: &Ctx,
        r: Request<pb::CreateVersionRequest>,
    ) -> Result<Response<pb::CreateVersionResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let rec = self
            .service
            .create_version(&ctx, &r.msg.name, &r.msg.from_version, &r.msg.new_version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::CreateVersionResponse {
            methodology: Some(to_pb(&rec)),
        }))
    }

    async fn delete_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteMethodologyRequest>,
    ) -> Result<Response<pb::DeleteMethodologyResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        self.service
            .delete(&ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::DeleteMethodologyResponse::default()))
    }

    async fn import_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImportMethodologyRequest>,
    ) -> Result<Response<pb::ImportMethodologyResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let (rec, issues) = self
            .service
            .import(&ctx, r.msg.yaml.as_bytes(), r.msg.publish)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::ImportMethodologyResponse {
            methodology: Some(to_pb(&rec)),
            issues: domainpb::issues_to_pb(&issues),
        }))
    }

    async fn export_methodology(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExportMethodologyRequest>,
    ) -> Result<Response<pb::ExportMethodologyResponse>, ConnectError> {
        let (out, filename) = self
            .service
            .export(ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::ExportMethodologyResponse {
            yaml: String::from_utf8_lossy(&out).into_owned(),
            filename,
        }))
    }

    async fn get_domain_usage(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetDomainUsageRequest>,
    ) -> Result<Response<pb::GetDomainUsageResponse>, ConnectError> {
        let rs = self
            .service
            .domain_usage(ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::GetDomainUsageResponse {
            methodologies: rs
                .iter()
                .map(|rec| pb::DomainUser {
                    name: rec.methodology.name.clone(),
                    version: rec.methodology.version.clone(),
                    status: rec.status.to_string(),
                })
                .collect(),
        }))
    }
}
