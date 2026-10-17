//! Port of `handler.go`: the Connect `registry.v1.DomainService`.

use std::sync::Arc;

use goap_algo::USAGE_NODE_VALIDATOR;
use goap_connect::{Code, ConnectError, Ctx, Request, Response, async_trait};
use goap_domainpb as domainpb;
use goap_gen::registry::v1::{self as pb, DomainService};
use goap_identity::Extractor;
use goap_pbconv as pbconv;

use crate::Service;
use crate::error::Error;

/// Handler implements the Connect `DomainService` (`goap_gen::registry::v1::domain_service_router`).
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    /// Identity extracts the caller from request headers.
    pub identity: Extractor,
}

impl Handler {
    pub fn new(service: Arc<Service>) -> Self {
        Handler {
            service,
            identity: Extractor::default(),
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
impl DomainService for Handler {
    async fn list_domains(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListDomainsRequest>,
    ) -> Result<Response<pb::ListDomainsResponse>, ConnectError> {
        let rs = self
            .service
            .domain_versions(ctx, r.msg.all_versions)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::ListDomainsResponse {
            domains: rs.iter().map(domainpb::domain_summary_to_pb).collect(),
        }))
    }

    async fn get_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetDomainRequest>,
    ) -> Result<Response<pb::GetDomainResponse>, ConnectError> {
        let rec = self
            .service
            .get_domain(ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::GetDomainResponse {
            domain: Some(domainpb::domain_to_pb(&rec)),
        }))
    }

    async fn save_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::SaveDomainRequest>,
    ) -> Result<Response<pb::SaveDomainResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let (rec, issues) = self
            .service
            .save_domain(&ctx, domainpb::domain_from_pb(r.msg.domain.as_ref()))
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::SaveDomainResponse {
            domain: Some(domainpb::domain_to_pb(&rec)),
            issues: domainpb::issues_to_pb(&issues),
        }))
    }

    async fn validate_domain(
        &self,
        _ctx: &Ctx,
        r: Request<pb::ValidateDomainRequest>,
    ) -> Result<Response<pb::ValidateDomainResponse>, ConnectError> {
        let d = domainpb::domain_from_pb(r.msg.domain.as_ref());
        Ok(Response::new(pb::ValidateDomainResponse {
            issues: domainpb::issues_to_pb(&d.validate()),
        }))
    }

    async fn publish_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::PublishDomainRequest>,
    ) -> Result<Response<pb::PublishDomainResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let rec = self
            .service
            .publish_domain(&ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::PublishDomainResponse {
            domain: Some(domainpb::domain_to_pb(&rec)),
        }))
    }

    async fn create_domain_version(
        &self,
        ctx: &Ctx,
        r: Request<pb::CreateDomainVersionRequest>,
    ) -> Result<Response<pb::CreateDomainVersionResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let rec = self
            .service
            .create_domain_version(&ctx, &r.msg.name, &r.msg.from_version, &r.msg.new_version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::CreateDomainVersionResponse {
            domain: Some(domainpb::domain_to_pb(&rec)),
        }))
    }

    async fn delete_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteDomainRequest>,
    ) -> Result<Response<pb::DeleteDomainResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        self.service
            .delete_domain(&ctx, &r.msg.name, &r.msg.version)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::DeleteDomainResponse {}))
    }

    async fn import_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImportDomainRequest>,
    ) -> Result<Response<pb::ImportDomainResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let (rec, issues) = self
            .service
            .import_domain(&ctx, r.msg.yaml.as_bytes(), r.msg.publish)
            .await
            .map_err(to_connect)?;
        Ok(Response::new(pb::ImportDomainResponse {
            domain: Some(domainpb::domain_to_pb(&rec)),
            issues: domainpb::issues_to_pb(&issues),
        }))
    }

    async fn export_domain(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExportDomainRequest>,
    ) -> Result<Response<pb::ExportDomainResponse>, ConnectError> {
        let (out, name) = self
            .service
            .export_domain(ctx, &r.msg.name, &r.msg.version, &r.msg.format)
            .await
            .map_err(to_connect)?;
        let media = if r.msg.format == "json" {
            "application/json"
        } else {
            "application/yaml"
        };
        Ok(Response::new(pb::ExportDomainResponse {
            yaml: String::from_utf8_lossy(&out).into_owned(),
            filename: name,
            media_type: media.into(),
        }))
    }

    async fn run_algorithm(
        &self,
        ctx: &Ctx,
        r: Request<pb::RunAlgorithmRequest>,
    ) -> Result<Response<pb::RunAlgorithmResponse>, ConnectError> {
        let Some(alg) = r.msg.algorithm.as_ref() else {
            return Err(ConnectError::invalid_argument("algorithm required"));
        };
        let ctx = self.identity.context(ctx, &r.headers);
        let algs = domainpb::algorithms_from_pb(std::slice::from_ref(alg));
        let res = self
            .service
            .run_algorithm(
                &ctx,
                &algs[0],
                &pbconv::to_map(r.msg.values.as_ref()),
                &pbconv::to_map(r.msg.input.as_ref()),
            )
            .await;
        match res {
            Err(e) if e.is_invalid() || e.is_forbidden() => Err(to_connect(e)),
            Err(e) => Ok(Response::new(pb::RunAlgorithmResponse {
                error: e.to_string(),
                ..Default::default()
            })),
            Ok(out) => Ok(Response::new(pb::RunAlgorithmResponse {
                ok: out.ok(),
                set: pbconv::to_struct(&out.set),
                unset: out.unset.clone(),
                logs: out
                    .logs
                    .iter()
                    .map(|l| format!("{}: {}", l.level, l.message))
                    .collect(),
                failures: out.failures,
                error: String::new(),
            })),
        }
    }

    async fn list_types(
        &self,
        ctx: &Ctx,
        _r: Request<pb::ListTypesRequest>,
    ) -> Result<Response<pb::ListTypesResponse>, ConnectError> {
        let cat = self.service.types(ctx, &[]).await.map_err(to_connect)?;
        let mut out = pb::ListTypesResponse {
            domains: cat.domains().into_iter().collect(),
            ..Default::default()
        };
        for t in cat.types() {
            let mut ti = pb::TypeInfo {
                r#ref: t.r#ref.to_string(),
                description: t.description.clone(),
                attributes: domainpb::attribute_infos_to_pb(&t.attributes),
                change_controlled: t.change_controlled,
                editor: t.editor.clone(),
                additional_properties: t.additional_properties,
                form: pbconv::form_to_pb(t.form.as_ref()),
                ancestors: t.ancestors.iter().map(|a| a.to_string()).collect(),
                ..Default::default()
            };
            if let Some(l) = &t.lifecycle {
                ti.lifecycle = domainpb::lifecycles_to_pb(std::slice::from_ref(l))
                    .into_iter()
                    .next();
            }
            if let Some(d) = &t.document {
                ti.contains = d.contains.clone();
            }
            ti.node_validators = t
                .validators
                .iter()
                .filter(|v| v.r#type == USAGE_NODE_VALIDATOR)
                .map(|v| v.instance.clone())
                .collect();
            out.types.push(ti);
        }
        for l in cat.link_types() {
            let mut li = pb::LinkTypeInfo {
                r#ref: l.r#ref.to_string(),
                compose: l.compose,
                attributes: domainpb::attribute_infos_to_pb(&l.attributes),
                label: l.label.clone(),
                inverse_label: l.inverse_label.clone(),
                ..Default::default()
            };
            if !l.from.is_zero() {
                li.from = l.from.to_string();
            }
            if !l.to.is_zero() {
                li.to = l.to.to_string();
            }
            out.link_types.push(li);
        }
        Ok(Response::new(out))
    }
}
