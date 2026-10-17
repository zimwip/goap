//! Port of `handler.go`: the Connect `index.v1.IndexService`.

use std::sync::Arc;

use goap_authz::{self as authz, Resource};
use goap_connect::{Code, ConnectError, Ctx, Request, Response, async_trait};
use goap_domain::NodeID;
use goap_gen::index::v1::{self as pb, IndexService};
use goap_identity::Extractor;
use goap_index::{self as index, KIND_CHANGE, KIND_NODE};
use goap_rpcerr as rpcerr;

use crate::service::{Service, dedup};

/// Handler implements the Connect `IndexService` (`goap_gen::index::v1::index_service_router`).
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    pub identity: Extractor,
    /// Authz guards Reindex (admin on platform); None grants it.
    pub authz: Option<Arc<dyn authz::Authorizer>>,
}

impl Handler {
    pub fn new(service: Arc<Service>) -> Self {
        Handler {
            service,
            identity: Extractor::default(),
            authz: None,
        }
    }
}

/// searchErr maps the errors of a search to Connect codes: a semantic search without embedding is a failed
/// precondition, so that a client can fall back to another mode.
fn search_err(err: index::Error) -> ConnectError {
    if err.is_semantic_unavailable() {
        return ConnectError::from_error(Code::FailedPrecondition, err);
    }
    if err.is_invalid() {
        return ConnectError::from_error(Code::InvalidArgument, err);
    }
    if err.is_not_found() {
        return ConnectError::from_error(Code::NotFound, err);
    }
    rpcerr::to_connect(err)
}

#[async_trait]
impl IndexService for Handler {
    async fn search(
        &self,
        ctx: &Ctx,
        r: Request<pb::SearchRequest>,
    ) -> Result<Response<pb::SearchResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let m = r.msg;
        for k in &m.kinds {
            if k != KIND_NODE && k != KIND_CHANGE {
                return Err(ConnectError::invalid_argument(format!(
                    "unknown kind {k:?} (node or change)"
                )));
            }
        }
        let mut q = index::Query {
            text: m.text.clone(),
            facets: m.facets.clone(),
            limit: m.limit.max(0) as usize,
            offset: m.offset.max(0) as usize,
            mode: m.mode.clone(),
            min_similarity: m.min_similarity,
            snippet: m.snippet,
            filter: index::Filter {
                kind: m.kinds.clone(),
                namespace: m.namespaces.clone(),
                r#type: m.types.clone(),
                state: m.states.clone(),
                branch: m.branches.clone(),
                main: m.main,
                project: m.projects.clone(),
                owner: m.owner_units.clone(),
                status: m.statuses.clone(),
                methodology: m.methodologies.clone(),
                roots_only: m.roots_only,
                ..index::Filter::default()
            },
            ..index::Query::default()
        };
        if m.include_subprojects
            && !m.projects.is_empty()
            && let Some(acc) = self.service.access()
        {
            let mut all = Vec::new();
            for p in &m.projects {
                let subs = acc
                    .sub_projects(&ctx, p)
                    .await
                    .map_err(rpcerr::to_connect)?;
                all.extend(subs);
            }
            q.filter.project = dedup(all);
        }
        if let Some(s) = &m.similar_to {
            if s.id.is_empty()
                || (!s.kind.is_empty() && s.kind != KIND_NODE && s.kind != KIND_CHANGE)
            {
                return Err(ConnectError::invalid_argument(
                    "similar_to needs a kind (node or change) and an id",
                ));
            }
            q.similar_to = Some(index::Ref {
                kind: s.kind.clone(),
                id: NodeID::from(s.id.clone()),
                ..index::Ref::default()
            });
        }
        for f in &m.facet_filters {
            q.filter.facets.insert(f.name.clone(), f.values.clone());
        }
        let res = self
            .service
            .searcher
            .search(&ctx, q)
            .await
            .map_err(search_err)?;
        let mut out = pb::SearchResponse {
            total: res.total as i32,
            semantic: res.semantic,
            truncated: res.truncated,
            ..Default::default()
        };
        for x in &res.hits {
            let mut hit = pb::Hit {
                kind: x.kind.clone(),
                id: x.id.to_string(),
                version: x.version as i32,
                namespace: x.namespace.clone(),
                r#type: x.r#type.clone(),
                key: x.key.clone(),
                state: x.state.clone(),
                branch: x.branch.clone(),
                main: x.main,
                deleted: x.deleted,
                project: x.project.clone(),
                owner_unit: x.owner.clone(),
                facets: x.facets.clone().into_iter().collect(),
                score: x.score,
                similarity: x.similarity,
                snippet: x.snippet.clone(),
                change: None,
            };
            if x.kind == KIND_CHANGE {
                hit.change = Some(pb::ChangeInfo {
                    change_id: x.id.to_string(),
                    title: x.title.clone(),
                    status: x.status.clone(),
                    project_id: x.project.clone(),
                    methodology: x.methodology.clone(),
                    owner_org: x.owner.clone(),
                    parent_id: x.parent.clone(),
                });
            }
            out.hits.push(hit);
        }
        for name in &m.facets {
            let counts = res
                .facets
                .get(name)
                .map(|cs| {
                    cs.iter()
                        .map(|c| pb::FacetCount {
                            value: c.value.clone(),
                            count: c.count as i32,
                        })
                        .collect()
                })
                .unwrap_or_default();
            out.facets.push(pb::FacetResult {
                name: name.clone(),
                counts,
            });
        }
        Ok(Response::new(out))
    }

    async fn reindex(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReindexRequest>,
    ) -> Result<Response<pb::ReindexResponse>, ConnectError> {
        let ctx = self.identity.context(ctx, &r.headers);
        let req = authz::Request {
            subject: authz::principal_from(&ctx),
            action: "admin".into(),
            resource: Resource {
                r#type: "platform".into(),
                ..Resource::default()
            },
        };
        authz::check(&ctx, self.authz.as_deref(), &req)
            .await
            .map_err(rpcerr::to_connect)?;
        let n = self
            .service
            .reindex(&ctx)
            .await
            .map_err(rpcerr::to_connect)?;
        Ok(Response::new(pb::ReindexResponse { versions: n as i32 }))
    }

    async fn status(
        &self,
        _ctx: &Ctx,
        _r: Request<pb::StatusRequest>,
    ) -> Result<Response<pb::StatusResponse>, ConnectError> {
        let s = self.service.status();
        Ok(Response::new(pb::StatusResponse {
            semantic: s.semantic,
            store: s.store,
            nodes_indexed: s.nodes,
            baselines_followed: s.baselines,
            changes_indexed: s.changes,
            errors: s.errors,
        }))
    }
}
