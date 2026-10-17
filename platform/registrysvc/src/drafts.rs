//! Port of `drafts.go`: the registry as the self-observation agent sees it (selfimprove.MethodologyDrafts).

use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::axum::http::{HeaderMap, HeaderValue};
use goap_connect::{Code, Ctx, Request};
use goap_def::{Issue, Issues};
use goap_gen::registry::v1 as pb;
use goap_methodology::Methodology;
use goap_selfimprove::{BoxError, MethodologyDrafts};

use crate::client::Client;
use crate::convert::{from_pb, to_pb};
use crate::service::Service;
use crate::store::{Record, STATUS_DRAFT};

/// Drafts adapts an in-process registry to selfimprove.MethodologyDrafts (the self-observation agent saves improved
/// versions as drafts).
#[derive(Clone)]
pub struct Drafts {
    pub service: Arc<Service>,
}

#[async_trait]
impl MethodologyDrafts for Drafts {
    async fn definition(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<Option<Methodology>, BoxError> {
        match self.service.store.get(ctx, name, version).await {
            Ok(r) => Ok(Some(r.methodology)),
            Err(e) if e.is_not_found() => Ok(None),
            Err(e) => Err(Box::new(e)),
        }
    }

    async fn save_draft(&self, ctx: &Ctx, m: Methodology) -> Result<Issues, BoxError> {
        let (_, issues) = self.service.save(ctx, m).await?;
        Ok(issues)
    }
}

/// withIdentity forwards the principal of ctx to the registry (service to service calls inside the platform network,
/// as the gateway does).
fn with_identity<T>(ctx: &Ctx, msg: T) -> Request<T> {
    let p = goap_authz::principal_from(ctx);
    let mut h = HeaderMap::new();
    for (name, v) in [
        (goap_identity::HEADER_SUBJECT, p.subject),
        (goap_identity::HEADER_ORG, p.org),
        (goap_identity::HEADER_ROLES, p.roles.join(",")),
    ] {
        if let Ok(v) = HeaderValue::from_str(&v) {
            h.insert(name, v);
        }
    }
    Request::with_headers(msg, h)
}

#[async_trait]
impl MethodologyDrafts for Client {
    async fn definition(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<Option<Methodology>, BoxError> {
        let req = with_identity(
            ctx,
            pb::GetMethodologyRequest {
                name: name.to_string(),
                version: version.to_string(),
                with_domains: false,
            },
        );
        match self.rpc.get_methodology(ctx, req).await {
            Err(e) if e.code() == Code::NotFound => Ok(None),
            Err(e) => Err(Box::new(e)),
            Ok(r) => Ok(Some(from_pb(r.msg.methodology.as_ref()))),
        }
    }

    async fn save_draft(&self, ctx: &Ctx, m: Methodology) -> Result<Issues, BoxError> {
        let req = with_identity(
            ctx,
            pb::SaveMethodologyRequest {
                methodology: Some(to_pb(&Record {
                    methodology: m,
                    status: STATUS_DRAFT,
                    ..Record::default()
                })),
            },
        );
        let r = self.rpc.save_methodology(ctx, req).await?;
        Ok(Issues(
            r.msg
                .issues
                .iter()
                .map(|i| Issue {
                    path: i.path.clone(),
                    message: i.message.clone(),
                    ..Issue::default()
                })
                .collect(),
        ))
    }
}
