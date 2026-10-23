//! `goap.registry.v1.DomainService` (Go's `graph/domainsvc`, ADR 0125 §3): the domain registry the graph serves. The
//! built-in domains come first, published and frozen, then the stored versions by name and creation.
use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::registry::v1 as pb;
use graph_catalog::{
    Catalog,
    catalog::{builtins, is_builtin},
    imported::{self, Imported},
    record::{Record, STATUS_DRAFT, STATUS_PUBLISHED, in_force, latest},
};
use graph_core::{GraphError, gojson, store::OutboxMessage};
use graph_store::read::NewDomain;

use crate::{Domains, convert, graph::connect_error};

/// The identity headers the gateway sets (`lib/identity`) and the stamp of a command (`X-Goap-Command`).
const HEADER_SUBJECT: &str = "x-goap-subject";
const HEADER_COMMAND: &str = "x-goap-command";

fn unserved(what: &str) -> ConnectError {
    connect_error(GraphError::unimplemented(format!("{what}: not served here yet")))
}

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

    /// Stores a domain version from its YAML, published on request (`Service.ImportDomain`): a new version a platform
    /// service imports, valid and with nothing the Go service alone checks. Anything else (a person, a version already
    /// stored, issues, a lifecycle, a script...) is the Go service's.
    async fn import_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImportDomainRequest>,
    ) -> Answer<pb::ImportDomainResponse> {
        let subject = req.header(HEADER_SUBJECT).to_string();
        let command = req.header(HEADER_COMMAND).to_string();
        if !subject.starts_with("system:") {
            return Err(unserved("a person's import of a domain goes through the authorizer"));
        }
        let r = req.into_inner();
        let Some(d) = Imported::parse(&r.yaml) else {
            return Err(unserved("a domain with issues, or checked by scripts, guards or forms"));
        };
        if d.version.is_empty() || is_builtin(&d.name) {
            return Err(unserved("a domain with no version, or a built-in one"));
        }
        let domain = d.domain().map_err(|e| connect_error(GraphError::internal(e)))?;
        let tx = self
            .state
            .store
            .writer()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        if tx
            .domain_status(&d.name, &d.version)
            .await
            .map_err(connect_error)?
            .is_some()
        {
            return Err(unserved("a domain version already stored"));
        }
        // the domains in force with this version in place of its domain's (`Service.Types`)
        let rs = tx.domain_records().await.map_err(connect_error)?;
        let mut ds: Vec<_> = in_force(&rs).into_iter().filter(|x| !is_builtin(&x.name)).collect();
        match ds.iter_mut().find(|x| x.name == d.name) {
            Some(x) => *x = domain,
            None => ds.push(domain),
        }
        if Catalog::new(&ds).is_err() {
            return Err(unserved(
                "a domain whose types do not resolve with the domains in force",
            ));
        }

        let now = chrono::Utc::now();
        let at = gojson::time_text(&now);
        let (status, event) = if r.publish {
            (STATUS_PUBLISHED, "published")
        } else {
            (STATUS_DRAFT, "saved")
        };
        let mut events = Vec::new();
        if self.state.outbox {
            let mut headers = std::collections::BTreeMap::new();
            if !command.is_empty() {
                headers.insert("X-Goap-Actor".to_string(), subject.clone());
                headers.insert("X-Goap-Command".to_string(), command);
            }
            events.push(OutboxMessage {
                subject: format!("goap.registry.domain.{event}"),
                data: gojson::to_string(&serde_json::json!({"name": d.name, "version": d.version, "status": status})),
                headers,
                created_at: now,
            });
        }
        let definition = gojson::to_string(&d);
        tx.insert_domain(
            &NewDomain {
                name: &d.name,
                version: &d.version,
                status,
                definition: &definition,
                created_at: &at,
                updated_at: &at,
                published_at: if r.publish { &at } else { "" },
                updated_by: &subject,
            },
            &events,
        )
        .await
        .map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;

        let published = r.publish.then_some(now);
        Ok(Response::new(pb::ImportDomainResponse {
            domain: Some(domain_pb(&d, status, Some(now), Some(now), published, &subject)),
            issues: Vec::new(),
        }))
    }
}

/// A stored version as the registry answers it (`domainpb.DomainToPB`).
fn domain_pb(
    d: &Imported,
    status: &str,
    created: Option<chrono::DateTime<chrono::Utc>>,
    updated: Option<chrono::DateTime<chrono::Utc>>,
    published: Option<chrono::DateTime<chrono::Utc>>,
    by: &str,
) -> pb::Domain {
    pb::Domain {
        name: d.name.clone(),
        version: d.version.clone(),
        description: d.description.clone(),
        status: status.into(),
        node_types: d
            .node_types()
            .iter()
            .map(|n| pb::NodeType {
                name: n.name.clone(),
                description: n.description.clone(),
                attributes: attributes(&n.attributes),
                extends: n.extends.clone(),
                change_controlled: n.change_controlled,
                search: n
                    .search
                    .iter()
                    .map(|s| pb::SearchProperty {
                        property: s.property.clone(),
                        text: s.text,
                        facet: s.facet,
                    })
                    .collect(),
                editor: n.editor.clone(),
                icon: n.icon.clone(),
                color: n.color.clone(),
                requires: n
                    .requires
                    .iter()
                    .map(|r| pb::RequiredLink {
                        link: r.link.clone(),
                        count: r.count as i32,
                    })
                    .collect(),
                admin_only: n.admin_only,
                additional_properties: n.additional_properties,
                ..pb::NodeType::default()
            })
            .collect(),
        link_types: d
            .link_types()
            .iter()
            .map(|l| pb::LinkType {
                name: l.name.clone(),
                description: l.description.clone(),
                from: l.from.clone(),
                to: l.to.clone(),
                compose: l.compose,
                attributes: attributes(&l.attributes),
                validators: Vec::new(),
                label: l.label.clone(),
                inverse_label: l.inverse_label.clone(),
            })
            .collect(),
        enums: d
            .enums
            .iter()
            .map(|e| pb::Enum {
                name: e.name.clone(),
                description: e.description.clone(),
                values: e
                    .values
                    .iter()
                    .map(|v| pb::EnumValue {
                        value: v.value.clone(),
                        label: v.label.clone(),
                    })
                    .collect(),
            })
            .collect(),
        item_types: d
            .item_types
            .iter()
            .map(|t| pb::ItemType {
                name: t.name.clone(),
                description: t.description.clone(),
                attributes: attributes(&t.attributes),
            })
            .collect(),
        importers: d
            .importers
            .iter()
            .map(|i| pb::ImporterDecl {
                name: i.name.clone(),
                label: i.label.clone(),
                description: i.description.clone(),
                params: attributes(&i.params),
            })
            .collect(),
        created_at: convert::time(&created),
        updated_at: convert::time(&updated),
        published_at: convert::time(&published),
        updated_by: by.into(),
        ..pb::Domain::default()
    }
}

fn attributes(as_: &[imported::Attribute]) -> Vec<pb::Attribute> {
    as_.iter()
        .map(|a| pb::Attribute {
            name: a.name.clone(),
            label: a.label.clone(),
            description: a.description.clone(),
            r#type: a.typ.clone(),
            widget: a.widget.clone(),
            diff: a.diff.clone(),
            r#enum: a.enumeration.clone(),
            default_value: a.default.clone(),
            section: a.section.clone(),
            order: a.order as i32,
            tooltip: a.tooltip.clone(),
            as_name: a.as_name,
            validators: Vec::new(),
        })
        .collect()
}
