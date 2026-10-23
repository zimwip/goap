//! `goap.registry.v1.DomainService` (Go's `graph/domainsvc`, ADR 0125 §3): the domain registry the graph serves. The
//! built-in domains come first, published and frozen, then the stored versions by name and creation.
use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::registry::v1 as pb;
use graph_catalog::{
    catalog::builtins,
    record::{Record, STATUS_PUBLISHED, latest},
};
use graph_core::GraphError;

use crate::{Domains, convert, graph::connect_error};

type Answer<T> = Result<Response<T>, ConnectError>;

/// The summary of a version (`domainpb.DomainSummaryToPB`).
fn summary(r: &Record, builtin: bool) -> pb::DomainSummary {
    let d = &r.domain;
    pb::DomainSummary {
        name: d.name.clone(),
        version: d.version.clone(),
        description: d.description.clone(),
        status: r.status.clone(),
        node_type_count: d.node_types.len() as i32,
        link_type_count: d.link_types.len() as i32,
        item_type_count: d.item_types.len() as i32,
        updated_at: convert::time(&r.updated_at),
        published_at: convert::time(&r.published_at),
        builtin,
    }
}

#[async_trait]
impl pb::DomainService for Domains {
    async fn list_domains(&self, _ctx: &Ctx, req: Request<pb::ListDomainsRequest>) -> Answer<pb::ListDomainsResponse> {
        let all = req.into_inner().all_versions;
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let mut stored = tx.domain_records().await.map_err(connect_error)?;
        stored.sort_by(|a, b| {
            a.domain
                .name
                .cmp(&b.domain.name)
                .then(a.created_at.unwrap_or_default().cmp(&b.created_at.unwrap_or_default()))
        });
        let mut rs: Vec<(Record, bool)> = builtins()
            .iter()
            .map(|d| {
                let r = Record {
                    domain: d.clone(),
                    status: STATUS_PUBLISHED.into(),
                    created_at: None,
                    updated_at: None,
                    published_at: None,
                    updated_by: "platform".into(),
                };
                (r, true)
            })
            .collect();
        rs.extend(stored.into_iter().map(|r| (r, false)));
        let records: Vec<Record> = rs.iter().map(|(r, _)| r.clone()).collect();
        let shown: Vec<&Record> = if all {
            records.iter().collect()
        } else {
            latest(&records)
        };
        let domains = shown
            .into_iter()
            .map(|r| {
                let builtin = rs.iter().any(|(b, is)| *is && b.domain.name == r.domain.name);
                summary(r, builtin)
            })
            .collect();
        Ok(Response::new(pb::ListDomainsResponse { domains }))
    }
}
