//! `goap.registry.v1.DomainService` (Go's `graph/domainsvc`, ADR 0125 §3): the domain registry the graph serves. The
//! built-in domains come first, published and frozen, then the stored versions by name and creation.
use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::registry::v1 as pb;
use graph_catalog::{
    Catalog,
    catalog::{builtin_sources, builtins, is_builtin},
    imported::{self, Imported},
    record::{Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, in_force, latest},
};
use graph_core::{GraphError, gojson, store::OutboxMessage};
use graph_store::read::{NewDomain, StoredDomain};

use crate::{Domains, convert, domainpb, graph::connect_error};

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

    /// A version (`Service.GetDomain`): a built-in domain (any version asked, or its own), else the one stored, else
    /// with no version the latest published.
    async fn get_domain(&self, _ctx: &Ctx, req: Request<pb::GetDomainRequest>) -> Answer<pb::GetDomainResponse> {
        let r = req.into_inner();
        for src in builtin_sources() {
            let def: serde_json::Value =
                serde_norway::from_str(src).map_err(|e| connect_error(GraphError::internal(e.to_string())))?;
            let mut d = domainpb::domain(&def).map_err(|e| connect_error(GraphError::internal(e)))?;
            if d.name == r.name && (r.version.is_empty() || r.version == d.version) {
                d.status = STATUS_PUBLISHED.into();
                d.updated_by = "platform".into();
                d.builtin = true;
                return Ok(Response::new(pb::GetDomainResponse { domain: Some(d) }));
            }
        }
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        let d = stored_pb(s)?;
        Ok(Response::new(pb::GetDomainResponse { domain: Some(d) }))
    }

    /// A stored version as JSON (`Service.ExportDomain` with format `json`): the YAML document of the definition read
    /// back, keys sorted, indented. The YAML itself (yaml.v3's emitter) and a built-in domain are the Go service's.
    async fn export_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ExportDomainRequest>,
    ) -> Answer<pb::ExportDomainResponse> {
        let r = req.into_inner();
        if is_builtin(&r.name) {
            return Err(unserved("the export of a built-in domain"));
        }
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(unserved("the export of a domain not stored"));
        };
        match r.format.as_str() {
            "json" => {}
            "" | "yaml" => return Err(unserved("the YAML export of a domain")),
            f => {
                return Err(ConnectError::invalid_argument(format!(
                    "unknown export format {f:?} (yaml or json): invalid domain"
                )));
            }
        }
        let mut def = s.definition;
        if let Some(o) = def.as_object_mut() {
            o.insert("name".into(), s.name.clone().into());
            o.insert("version".into(), s.version.clone().into());
        }
        Ok(Response::new(pb::ExportDomainResponse {
            yaml: gojson::to_string_indent(&yaml_document(def), "  "),
            filename: format!("domain-{}-{}.json", s.name, s.version),
            media_type: "application/json".into(),
        }))
    }

    /// The resolved model of the types in force (`Handler.ListTypes`).
    async fn list_types(&self, _ctx: &Ctx, _req: Request<pb::ListTypesRequest>) -> Answer<pb::ListTypesResponse> {
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let rs = tx.domain_records().await.map_err(connect_error)?;
        let ds: Vec<_> = in_force(&rs).into_iter().filter(|x| !is_builtin(&x.name)).collect();
        // the domains in force resolve, or the registry refused them; a catalogue that does not is Go's to word
        let cat = Catalog::new(&ds).map_err(|_| unserved("a catalogue that does not resolve"))?;
        let types = cat
            .types()
            .into_iter()
            .map(|t| pb::TypeInfo {
                r#ref: t.reference.clone(),
                description: t.description.clone(),
                attributes: domainpb::attribute_infos(&t.attributes),
                ancestors: t.ancestors.clone(),
                lifecycle: t.lifecycle.as_deref().map(domainpb::lifecycle_info),
                change_controlled: t.change_controlled,
                editor: t.editor.clone(),
                contains: t.document.clone().unwrap_or_default(),
                node_validators: t.node_validators.clone(),
                additional_properties: t.additional_properties,
                form: t.form.as_ref().and_then(|f| f.as_object()).map(convert::object),
                icon: t.icon.clone(),
                color: t.color.clone(),
            })
            .collect();
        let link_types = cat
            .link_types()
            .into_iter()
            .map(|l| pb::LinkTypeInfo {
                r#ref: l.reference.clone(),
                from: l.from.clone(),
                to: l.to.clone(),
                compose: l.compose,
                attributes: domainpb::attribute_infos(&l.attributes),
                label: l.label.clone(),
                inverse_label: l.inverse_label.clone(),
            })
            .collect();
        Ok(Response::new(pb::ListTypesResponse {
            types,
            link_types,
            domains: cat.domains().clone().into_iter().collect(),
        }))
    }

    /// Checks an edited domain (`Handler.ValidateDomain`): one with no issue and nothing the Go service alone checks.
    async fn validate_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ValidateDomainRequest>,
    ) -> Answer<pb::ValidateDomainResponse> {
        let p = req.into_inner().domain.unwrap_or_default();
        if domainpb::imported(&p).is_none() {
            return Err(unserved("a domain with issues, or checked by scripts, guards or forms"));
        }
        Ok(Response::new(pb::ValidateDomainResponse { issues: Vec::new() }))
    }

    /// Creates or replaces a draft (`Service.SaveDomain`): a platform service's, with no issue and nothing the Go
    /// service alone checks (a draft with issues is stored by Go, which words them).
    async fn save_domain(&self, _ctx: &Ctx, req: Request<pb::SaveDomainRequest>) -> Answer<pb::SaveDomainResponse> {
        let (subject, command) = caller(&req);
        let p = req.into_inner().domain.unwrap_or_default();
        if p.name.is_empty() || p.version.is_empty() {
            return Err(ConnectError::invalid_argument(
                "name and version are required: invalid domain",
            ));
        }
        if is_builtin(&p.name) {
            return Err(builtin_error(&p.name));
        }
        if !subject.starts_with("system:") {
            return Err(unserved("a person's draft of a domain goes through the authorizer"));
        }
        let Some(d) = domainpb::imported(&p) else {
            return Err(unserved("a domain with issues, or checked by scripts, guards or forms"));
        };
        let domain = d.domain().map_err(|e| connect_error(GraphError::internal(e)))?;
        let tx = self.writer().await?;
        if !resolves(&tx, domain).await? {
            return Err(unserved(
                "a domain whose types do not resolve with the domains in force",
            ));
        }
        let stored = tx.domain_status(&d.name, &d.version).await.map_err(connect_error)?;
        if stored.as_deref().is_some_and(|s| s != STATUS_DRAFT) {
            return Err(not_a_draft(&format!("{}@{}", d.name, d.version)));
        }
        let now = chrono::Utc::now();
        let at = gojson::time_text(&now);
        let events = self.events(&subject, &command, "saved", &d.name, &d.version, STATUS_DRAFT, now);
        let definition = gojson::to_string(&d);
        let row = NewDomain {
            name: &d.name,
            version: &d.version,
            status: STATUS_DRAFT,
            definition: &definition,
            created_at: &at,
            updated_at: &at,
            published_at: "",
            updated_by: &subject,
        };
        if stored.is_some() {
            tx.update_domain(&row, &events).await
        } else {
            tx.insert_domain(&row, &events).await
        }
        .map_err(connect_error)?;
        let saved = tx.stored_domain(&d.name, &d.version).await.map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        let saved = saved.ok_or_else(|| not_found(&d.name, &d.version))?;
        Ok(Response::new(pb::SaveDomainResponse {
            domain: Some(stored_pb(saved)?),
            issues: Vec::new(),
        }))
    }

    /// Freezes a draft (`Service.PublishDomain`): a version a platform service publishes, valid and with nothing the
    /// Go service alone checks.
    async fn publish_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::PublishDomainRequest>,
    ) -> Answer<pb::PublishDomainResponse> {
        let (subject, command) = caller(&req);
        let r = req.into_inner();
        if is_builtin(&r.name) {
            return Err(builtin_error(&r.name));
        }
        let tx = self.writer().await?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        if !subject.starts_with("system:") {
            return Err(unserved(
                "a person's publication of a domain goes through the authorizer",
            ));
        }
        if s.status != STATUS_DRAFT {
            return Err(not_a_draft(&format!("{}@{}", r.name, r.version)));
        }
        let Some(d) = Imported::parse(&s.definition_text) else {
            return Err(unserved("a domain with issues, or checked by scripts, guards or forms"));
        };
        let domain = d.domain().map_err(|e| connect_error(GraphError::internal(e)))?;
        if !resolves(&tx, domain).await? {
            return Err(unserved(
                "a domain whose types do not resolve with the domains in force",
            ));
        }
        let now = chrono::Utc::now();
        let events = self.events(
            &subject,
            &command,
            "published",
            &r.name,
            &r.version,
            STATUS_PUBLISHED,
            now,
        );
        let at = gojson::time_text(&now);
        if !tx
            .set_domain_status(&r.name, &r.version, STATUS_PUBLISHED, &at, &events)
            .await
            .map_err(connect_error)?
        {
            return Err(not_found(&r.name, &r.version));
        }
        let saved = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        let saved = saved.ok_or_else(|| not_found(&r.name, &r.version))?;
        Ok(Response::new(pb::PublishDomainResponse {
            domain: Some(stored_pb(saved)?),
        }))
    }

    /// Copies a version into a new draft (`Service.CreateDomainVersion`), for a platform service.
    async fn create_domain_version(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CreateDomainVersionRequest>,
    ) -> Answer<pb::CreateDomainVersionResponse> {
        let (subject, command) = caller(&req);
        let r = req.into_inner();
        if is_builtin(&r.name) {
            return Err(builtin_error(&r.name));
        }
        let tx = self.writer().await?;
        let Some(src) = tx
            .stored_domain(&r.name, &r.from_version)
            .await
            .map_err(connect_error)?
        else {
            return Err(not_found(&r.name, &r.from_version));
        };
        let to = r.new_version.as_str();
        if to.is_empty() || to == src.version {
            return Err(ConnectError::invalid_argument(
                "a new version number is required: invalid domain",
            ));
        }
        if tx.domain_status(&r.name, to).await.map_err(connect_error)?.is_some() {
            return Err(not_a_draft(&format!("{}@{to} already exists", r.name)));
        }
        if !subject.starts_with("system:") {
            return Err(unserved("a person's new domain version goes through the authorizer"));
        }
        let definition = renumbered(&src, to);
        let now = chrono::Utc::now();
        let at = gojson::time_text(&now);
        let events = self.events(&subject, &command, "saved", &r.name, to, STATUS_DRAFT, now);
        tx.insert_domain(
            &NewDomain {
                name: &r.name,
                version: to,
                status: STATUS_DRAFT,
                definition: &definition,
                created_at: &at,
                updated_at: &at,
                published_at: "",
                updated_by: &subject,
            },
            &events,
        )
        .await
        .map_err(connect_error)?;
        let saved = tx.stored_domain(&r.name, to).await.map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        let saved = saved.ok_or_else(|| not_found(&r.name, to))?;
        Ok(Response::new(pb::CreateDomainVersionResponse {
            domain: Some(stored_pb(saved)?),
        }))
    }

    /// Removes a draft or archives a published version (`Service.DeleteDomain`), for a platform service.
    async fn delete_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::DeleteDomainRequest>,
    ) -> Answer<pb::DeleteDomainResponse> {
        let (subject, command) = caller(&req);
        let r = req.into_inner();
        if is_builtin(&r.name) {
            return Err(builtin_error(&r.name));
        }
        let tx = self.writer().await?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        if !subject.starts_with("system:") {
            return Err(unserved("a person's deletion of a domain goes through the authorizer"));
        }
        if s.status == STATUS_ARCHIVED {
            return Ok(Response::new(pb::DeleteDomainResponse {}));
        }
        // the event names the status the version had
        let events = self.events(
            &subject,
            &command,
            "deleted",
            &r.name,
            &r.version,
            &s.status,
            chrono::Utc::now(),
        );
        if s.status == STATUS_DRAFT {
            tx.delete_domain_draft(&r.name, &r.version, &events)
                .await
                .map_err(connect_error)?;
        } else {
            let at = gojson::time_text(&chrono::Utc::now());
            if !tx
                .set_domain_status(&r.name, &r.version, STATUS_ARCHIVED, &at, &events)
                .await
                .map_err(connect_error)?
            {
                return Err(ConnectError::not_found(format!(
                    "{}@{}: domain not found",
                    r.name, r.version
                )));
            }
        }
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        Ok(Response::new(pb::DeleteDomainResponse {}))
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
        if !resolves(&tx, domain.clone()).await? {
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
        let events = self.events(&subject, &command, event, &d.name, &d.version, status, now);
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

impl Domains {
    async fn writer(&self) -> Result<graph_store::read::Session, ConnectError> {
        self.state
            .store
            .writer()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))
    }

    /// The event of a write (`Service.events`), when the service has an outbox.
    #[allow(clippy::too_many_arguments)]
    fn events(
        &self,
        subject: &str,
        command: &str,
        event: &str,
        name: &str,
        version: &str,
        status: &str,
        now: chrono::DateTime<chrono::Utc>,
    ) -> Vec<OutboxMessage> {
        if !self.state.outbox {
            return Vec::new();
        }
        let mut headers = std::collections::BTreeMap::new();
        if !command.is_empty() {
            headers.insert("X-Goap-Actor".to_string(), subject.to_string());
            headers.insert("X-Goap-Command".to_string(), command.to_string());
        }
        vec![OutboxMessage {
            subject: format!("goap.registry.domain.{event}"),
            data: gojson::to_string(&serde_json::json!({"name": name, "version": version, "status": status})),
            headers,
            created_at: now,
        }]
    }
}

/// The subject and the command stamp of a call.
fn caller<T>(req: &Request<T>) -> (String, String) {
    (
        req.header(HEADER_SUBJECT).to_string(),
        req.header(HEADER_COMMAND).to_string(),
    )
}

/// A write to a built-in domain (`builtinError`).
fn builtin_error(name: &str) -> ConnectError {
    ConnectError::failed_precondition(format!(
        "domain {name} is built into the platform: it changes with the platform code"
    ))
}

/// A version the registry does not hold (`SQLStore.GetDomain`).
fn not_found(name: &str, version: &str) -> ConnectError {
    let what = if version.is_empty() {
        format!("{name} (published)")
    } else {
        format!("{name}@{version}")
    };
    ConnectError::not_found(format!("{what}: domain not found"))
}

/// A write a version that is no longer a draft refuses (`ErrImmutable`).
fn not_a_draft(what: &str) -> ConnectError {
    ConnectError::failed_precondition(format!("domain {what}: domain version is not a draft"))
}

/// Whether the domains in force still make a catalogue with this one in place of its domain's (`Service.Types`).
async fn resolves(tx: &graph_store::read::Session, d: graph_catalog::def::Domain) -> Result<bool, ConnectError> {
    let rs = tx.domain_records().await.map_err(connect_error)?;
    let mut ds: Vec<_> = in_force(&rs).into_iter().filter(|x| !is_builtin(&x.name)).collect();
    match ds.iter_mut().find(|x| x.name == d.name) {
        Some(x) => *x = d,
        None => ds.push(d),
    }
    Ok(Catalog::new(&ds).is_ok())
}

/// The definition of a version under another number, as Go writes it again: its JSON with the version replaced in
/// place (Go's JSON starts with the name and the version), else read and written again (the `jsonb` of PostgreSQL,
/// which orders the keys itself).
fn renumbered(src: &StoredDomain, to: &str) -> String {
    let head = format!(
        "{{\"name\":{},\"version\":{}",
        gojson::to_string(&src.name),
        gojson::to_string(&src.version)
    );
    if let Some(rest) = src.definition_text.strip_prefix(&head) {
        return format!(
            "{{\"name\":{},\"version\":{}{rest}",
            gojson::to_string(&src.name),
            gojson::to_string(&to)
        );
    }
    let mut def = src.definition.clone();
    if let Some(o) = def.as_object_mut() {
        o.insert("version".into(), to.into());
    }
    gojson::to_string(&def)
}

/// A stored version as the registry answers it (`domainpb.DomainToPB`).
fn stored_pb(s: StoredDomain) -> Result<pb::Domain, ConnectError> {
    let mut d = domainpb::domain(&s.definition).map_err(|e| connect_error(GraphError::internal(e)))?;
    d.name = s.name;
    d.version = s.version;
    d.status = s.status;
    d.created_at = convert::time(&s.created_at);
    d.updated_at = convert::time(&s.updated_at);
    d.published_at = convert::time(&s.published_at);
    d.updated_by = s.updated_by;
    Ok(d)
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

/// The stored JSON of a definition as its YAML document reads (`yaml.Marshal(def.Domain)` read back): the lists Go
/// writes `null` in JSON are `[]` in YAML, a transition's empty `requires` and an empty form are left out (YAML's
/// `omitempty` leaves out a zero struct, JSON's does not), and the bound algorithms of a transition never are YAML.
fn yaml_document(mut def: serde_json::Value) -> serde_json::Value {
    use serde_json::Value;
    fn list(o: &mut serde_json::Map<String, Value>, k: &str) {
        if o.get(k).is_none_or(Value::is_null) {
            o.insert(k.into(), Value::Array(Vec::new()));
        }
    }
    fn empty(v: Option<&Value>) -> bool {
        v.is_none_or(|v| v.is_null() || v.as_array().is_some_and(Vec::is_empty) || v.as_str() == Some(""))
    }
    let Some(d) = def.as_object_mut() else {
        return def;
    };
    list(d, "nodeTypes");
    list(d, "linkTypes");
    for n in d
        .get_mut("nodeTypes")
        .and_then(Value::as_array_mut)
        .into_iter()
        .flatten()
    {
        let Some(n) = n.as_object_mut() else { continue };
        if let Some(doc) = n.get_mut("document").and_then(Value::as_object_mut) {
            list(doc, "contains");
        }
        if n.get("form")
            .and_then(Value::as_object)
            .is_some_and(|f| empty(f.get("fields")) && empty(f.get("title")) && empty(f.get("description")))
        {
            n.remove("form");
        }
    }
    for l in d
        .get_mut("lifecycles")
        .and_then(Value::as_array_mut)
        .into_iter()
        .flatten()
    {
        let Some(l) = l.as_object_mut() else { continue };
        list(l, "states");
        for t in l
            .get_mut("transitions")
            .and_then(Value::as_array_mut)
            .into_iter()
            .flatten()
        {
            let Some(t) = t.as_object_mut() else { continue };
            t.remove("guardAlgos");
            t.remove("actionAlgos");
            if t.get("requires")
                .is_none_or(|r| empty(r.get("attributes")) && empty(r.get("outgoingLinks")))
            {
                t.remove("requires");
            }
            if let Some(c) = t.get_mut("children").and_then(Value::as_object_mut) {
                list(c, "states");
            }
        }
    }
    def
}

#[cfg(test)]
mod tests {
    use super::*;

    fn stored(text: &str) -> StoredDomain {
        StoredDomain {
            name: "dt".into(),
            version: "1.0.0".into(),
            status: STATUS_DRAFT.into(),
            definition: serde_json::from_str(text).unwrap(),
            definition_text: text.into(),
            created_at: None,
            updated_at: None,
            published_at: None,
            updated_by: String::new(),
        }
    }

    #[test]
    fn a_new_version_keeps_the_text_go_wrote() {
        let go = r#"{"name":"dt","version":"1.0.0","nodeTypes":[{"name":"Thing"}],"description":"x"}"#;
        assert_eq!(
            renumbered(&stored(go), "1.1.0"),
            r#"{"name":"dt","version":"1.1.0","nodeTypes":[{"name":"Thing"}],"description":"x"}"#
        );
        // the text of a jsonb, ordered by the database: read and written again
        let pg = r#"{"name": "dt", "version": "1.0.0", "nodeTypes": [{"name": "Thing"}]}"#;
        let again: serde_json::Value = serde_json::from_str(&renumbered(&stored(pg), "2.0.0")).unwrap();
        assert_eq!(again["version"], "2.0.0");
        assert_eq!(again["nodeTypes"][0]["name"], "Thing");
    }
}
