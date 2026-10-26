//! `goap.registry.v1.DomainService` (Go's `graph/domainsvc`, ADR 0125 §3, ADR 0167): the domain registry, a service of
//! its own. The built-in domains are stored versions like any other, seeded at each start and protected: a protected
//! domain is refused every write over RPC, the local command line of `goap-domains-rs` writes it.
use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::registry::v1 as pb;
use graph_catalog::{
    Catalog,
    imported::Definition,
    record::{Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, in_force, latest},
    validate::Issue,
};
use graph_core::{GraphError, jsontext, store::OutboxMessage, yaml};
use graph_store::read::{NewDomain, StoredDomain};

use crate::{
    Domains, convert, domainpb, domainyaml,
    graph::{connect_error, forbidden, person_of},
};

/// The identity headers the gateway sets (`lib/identity`) and the stamp of a command (`X-Goap-Command`).
const HEADER_SUBJECT: &str = "x-goap-subject";
const HEADER_COMMAND: &str = "x-goap-command";

/// A request whose definition does not read as a domain (Rust's words: Go's JSON encoder and reader never get there).
fn unreadable(e: impl std::fmt::Display) -> ConnectError {
    ConnectError::invalid_argument(format!("invalid domain: {e}"))
}

type Answer<T> = Result<Response<T>, ConnectError>;

/// The summary of a version (`domainpb.DomainSummaryToPB`).
fn summary(r: &Record, protected: bool) -> pb::DomainSummary {
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
        protected,
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
        let protected = tx.protected_domains().await.map_err(connect_error)?;
        let shown: Vec<&Record> = if all { stored.iter().collect() } else { latest(&stored) };
        let domains = shown
            .into_iter()
            .map(|r| summary(r, protected.contains(&r.domain.name)))
            .collect();
        Ok(Response::new(pb::ListDomainsResponse { domains }))
    }

    /// A version (`Service.GetDomain`): the one stored, else with no version the latest published.
    async fn get_domain(&self, _ctx: &Ctx, req: Request<pb::GetDomainRequest>) -> Answer<pb::GetDomainResponse> {
        let r = req.into_inner();
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        let protected = tx.protected_domains().await.map_err(connect_error)?;
        let mut d = stored_pb(s)?;
        d.protected = protected.contains(&d.name);
        Ok(Response::new(pb::GetDomainResponse { domain: Some(d) }))
    }

    /// A version as YAML or JSON (`Service.ExportDomain`): its YAML as `Domain.YAML` writes it (`domainyaml`), the
    /// JSON the YAML document read back, keys sorted, indented, written from its stored definition.
    async fn export_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ExportDomainRequest>,
    ) -> Answer<pb::ExportDomainResponse> {
        let r = req.into_inner();
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        let mut def = s.definition;
        if let Some(o) = def.as_object_mut() {
            o.insert("name".into(), s.name.clone().into());
            o.insert("version".into(), s.version.clone().into());
        }
        let (name, version) = (s.name, s.version);
        let yaml = yaml::to_string(&domainyaml::document(&def));
        let base = format!("domain-{name}-{version}");
        match r.format.as_str() {
            "" | "yaml" => Ok(Response::new(pb::ExportDomainResponse {
                yaml,
                filename: format!("{base}.yaml"),
                media_type: "application/yaml".into(),
            })),
            "json" => {
                let doc = yaml_document(def);
                Ok(Response::new(pb::ExportDomainResponse {
                    yaml: jsontext::to_string_pretty(&doc),
                    filename: format!("{base}.json"),
                    media_type: "application/json".into(),
                }))
            }
            f => Err(ConnectError::invalid_argument(format!(
                "unknown export format {f:?} (yaml or json): invalid domain"
            ))),
        }
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
        // the domains in force resolve, or the registry refused them; a catalogue that does not is an internal error
        let cat = Catalog::of(&in_force(&rs)).map_err(|e| connect_error(GraphError::internal(e)))?;
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

    /// Checks an edited domain (`Handler.ValidateDomain`): its issues (`Domain.Validate`), none of the catalogue's.
    async fn validate_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ValidateDomainRequest>,
    ) -> Answer<pb::ValidateDomainResponse> {
        let p = req.into_inner().domain.unwrap_or_default();
        let json = domainpb::definition(&p).map_err(unreadable)?;
        let domain = graph_catalog::Domain::from_json(&json).map_err(unreadable)?;
        let issues = graph_catalog::validate::validate(&domain);
        Ok(Response::new(pb::ValidateDomainResponse {
            issues: issues_pb(issues),
        }))
    }

    /// Runs an algorithm on a sample input (`Service.RunAlgorithm`): a platform service's; a declaration or parameter
    /// value with an issue is refused, a script that fails answers its error.
    async fn run_algorithm(
        &self,
        _ctx: &Ctx,
        req: Request<pb::RunAlgorithmRequest>,
    ) -> Answer<pb::RunAlgorithmResponse> {
        let who = person_of(&req);
        let r = req.into_inner();
        let Some(a) = r.algorithm else {
            return Err(ConnectError::invalid_argument("algorithm required"));
        };
        self.authorize(who.as_ref(), "write", "", "").await?;
        let a = algorithm(&a);
        let invalid = |m: String| Err(ConnectError::invalid_argument(format!("invalid domain: {m}")));
        if let Some(i) = a.issues().first() {
            return invalid(i.clone());
        }
        if let Err(e) = graph_script::js::check_code(&a.language, &a.code) {
            return invalid(e);
        }
        let values = r.values.as_ref().map(convert::map);
        let (params, issues) = a.resolve(values.as_ref());
        if let Some(i) = issues.first() {
            return invalid(i.clone());
        }
        let input = r.input.as_ref().map(convert::map).unwrap_or_default();
        // the input read into the fields of `dsl.AlgorithmInput`
        let sample = match serde_json::from_value::<Sample>(serde_json::Value::Object(input)) {
            Ok(s) => s,
            Err(e) => return invalid(format!("sample input: {e}")),
        };
        let b = graph_script::js::Bound {
            instance: "try".into(),
            algorithm: a.name.clone(),
            typ: a.typ.clone(),
            language: a.language.clone(),
            code: a.code.clone(),
            params,
            property: String::new(),
        };
        let (out, err) = graph_script::js::try_algorithm(&b, &sample.input());
        let resp = pb::RunAlgorithmResponse {
            ok: err.is_none() && out.ok(),
            logs: out.logs.iter().map(|l| format!("{}: {}", l.level, l.message)).collect(),
            set: (!out.set.is_empty()).then(|| convert::object(&out.set)),
            failures: out.failures,
            unset: out.unset,
            error: err.unwrap_or_default(),
        };
        Ok(Response::new(resp))
    }

    /// Creates or replaces a draft (`Service.SaveDomain`), read as `domainpb.DomainFromPB` reads it, with its issues
    /// (a draft is stored with them). A check only Go words (a guard cel-go may refuse, a script goja may refuse...) is
    /// the Go service's.
    async fn save_domain(&self, _ctx: &Ctx, req: Request<pb::SaveDomainRequest>) -> Answer<pb::SaveDomainResponse> {
        let (subject, command) = caller(&req);
        let who = person_of(&req);
        let p = req.into_inner().domain.unwrap_or_default();
        if p.name.is_empty() || p.version.is_empty() {
            return Err(ConnectError::invalid_argument(
                "name and version are required: invalid domain",
            ));
        }
        self.writable(&p.name, false).await?;
        self.authorize(who.as_ref(), "write", &p.name, &p.version).await?;
        let json = domainpb::definition(&p).map_err(unreadable)?;
        let domain = graph_catalog::Domain::from_json(&json).map_err(|e| connect_error(GraphError::internal(e)))?;
        let tx = self.writer().await?;
        let issues = issues_of(&tx, &domain).await?;
        let saved = self
            .store(tx, &p.name, &p.version, &json, STATUS_DRAFT, (&subject, &command))
            .await?;
        Ok(Response::new(pb::SaveDomainResponse {
            domain: Some(stored_pb(saved)?),
            issues: issues_pb(issues),
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
        let who = person_of(&req);
        let r = req.into_inner();
        self.writable(&r.name, false).await?;
        let tx = self.writer().await?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        self.authorize(who.as_ref(), "publish", &r.name, &r.version).await?;
        if s.status != STATUS_DRAFT {
            return Err(not_a_draft(&format!("{}@{}", r.name, r.version)));
        }
        let domain =
            graph_catalog::Domain::from_json(&s.definition_text).map_err(|e| connect_error(GraphError::internal(e)))?;
        let issues = issues_of(&tx, &domain).await?;
        if !issues.is_empty() {
            return Err(ConnectError::invalid_argument(format!(
                "invalid domain: {}",
                issues_text(&issues)
            )));
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
        let at = jsontext::time_text(&now);
        if !tx
            .set_domain_status(&r.name, &r.version, STATUS_PUBLISHED, &at, &events)
            .await
            .map_err(connect_error)?
        {
            return Err(not_found(&r.name, &r.version));
        }
        let saved = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        self.state.deliver_domains().await;
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
        let who = person_of(&req);
        let r = req.into_inner();
        self.writable(&r.name, false).await?;
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
        self.authorize(who.as_ref(), "write", &r.name, to).await?;
        let definition = renumbered(&src, to);
        let now = chrono::Utc::now();
        let at = jsontext::time_text(&now);
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
        self.state.deliver_domains().await;
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
        let who = person_of(&req);
        let r = req.into_inner();
        self.writable(&r.name, false).await?;
        let tx = self.writer().await?;
        let Some(s) = tx.stored_domain(&r.name, &r.version).await.map_err(connect_error)? else {
            return Err(not_found(&r.name, &r.version));
        };
        self.authorize(who.as_ref(), "delete", &r.name, &r.version).await?;
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
            let at = jsontext::time_text(&chrono::Utc::now());
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
        self.state.deliver_domains().await;
        Ok(Response::new(pb::DeleteDomainResponse {}))
    }

    /// Stores a domain version from its YAML, published on request (`Service.ImportDomain`): read as `def.ParseDomain`
    /// reads it (a definition that does not read is refused in Go's words), then stored as a draft or published; a
    /// domain with issues is kept as a draft, the issues in the error when it was to be published.
    async fn import_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImportDomainRequest>,
    ) -> Answer<pb::ImportDomainResponse> {
        let (subject, command) = caller(&req);
        let who = person_of(&req);
        let r = req.into_inner();
        let def =
            Definition::read(&r.yaml).map_err(|e| ConnectError::invalid_argument(format!("invalid domain: {e}")))?;
        if def.name.is_empty() || def.version.is_empty() {
            return Err(ConnectError::invalid_argument(
                "name and version are required: invalid domain",
            ));
        }
        self.writable(&def.name, false).await?;
        self.authorize(who.as_ref(), "write", &def.name, &def.version).await?;
        if r.publish {
            self.authorize(who.as_ref(), "publish", &def.name, &def.version).await?;
        }
        let json = def.json.as_ref().map_err(|e| connect_error(GraphError::internal(e)))?;
        let domain = graph_catalog::Domain::from_json(json).map_err(|e| connect_error(GraphError::internal(e)))?;
        let tx = self.writer().await?;
        let issues = issues_of(&tx, &domain).await?;
        let draft = !r.publish || !issues.is_empty();
        let status = if draft { STATUS_DRAFT } else { STATUS_PUBLISHED };
        let saved = self
            .store(tx, &def.name, &def.version, json, status, (&subject, &command))
            .await?;
        if r.publish && !issues.is_empty() {
            // kept as a draft, the issues in the error
            return Err(ConnectError::invalid_argument(format!(
                "invalid domain: cannot publish: {}",
                issues_text(&issues)
            )));
        }
        Ok(Response::new(pb::ImportDomainResponse {
            domain: Some(stored_pb(saved)?),
            issues: issues_pb(issues),
        }))
    }
}

impl Domains {
    /// Writes a version (`SQLStore.SaveDomain`): a new one inserted, a draft replaced (its creation kept), anything else
    /// refused; the event `saved` for a draft, `published` for a version published at once. The version as stored.
    async fn store(
        &self,
        tx: graph_store::read::Session,
        name: &str,
        version: &str,
        definition: &str,
        status: &str,
        (subject, command): (&str, &str),
    ) -> Result<StoredDomain, ConnectError> {
        let stored = tx.domain_status(name, version).await.map_err(connect_error)?;
        if stored.as_deref().is_some_and(|s| s != STATUS_DRAFT) {
            return Err(not_a_draft(&format!("{name}@{version}")));
        }
        let now = chrono::Utc::now();
        let at = jsontext::time_text(&now);
        let event = if status == STATUS_PUBLISHED {
            "published"
        } else {
            "saved"
        };
        let events = self.events(subject, command, event, name, version, status, now);
        let row = NewDomain {
            name,
            version,
            status,
            definition,
            created_at: &at,
            updated_at: &at,
            published_at: if status == STATUS_PUBLISHED { &at } else { "" },
            updated_by: subject,
        };
        if stored.is_some() {
            tx.update_domain(&row, &events).await
        } else {
            tx.insert_domain(&row, &events).await
        }
        .map_err(connect_error)?;
        let saved = tx.stored_domain(name, version).await.map_err(connect_error)?;
        tx.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        self.state.deliver_domains().await;
        saved.ok_or_else(|| not_found(name, version))
    }

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
            data: jsontext::to_string(&serde_json::json!({"name": name, "version": version, "status": status})),
            headers,
            created_at: now,
        }]
    }
}

impl Domains {
    /// An action of a person on a domain version, as the organisation's rules allow it (Go's
    /// `Service.authorizeDomain`): a platform service acting by itself is not asked.
    async fn authorize(
        &self,
        who: Option<&graph_core::access::Principal>,
        act: &str,
        name: &str,
        version: &str,
    ) -> Result<(), ConnectError> {
        let Some(who) = who else {
            return Ok(());
        };
        // the organisation is the platform graph's: read there (ADR 0167)
        let tx = self.state.reader().await.map_err(connect_error)?;
        let snap = graph_core::access::Snapshot::read(&tx).await.map_err(connect_error)?;
        let id = format!("{name}@{version}");
        let res = graph_script::abac::Resource {
            typ: "domain".into(),
            id: id.clone(),
            name: name.into(),
            org: who.org.clone(),
            ..Default::default()
        };
        if snap.authorize(who, res, act).map_err(connect_error)? {
            return Ok(());
        }
        Err(connect_error(forbidden(&who.subject, act, "domain", &id)))
    }
}

/// The subject and the command stamp of a call.
fn caller<T>(req: &Request<T>) -> (String, String) {
    (
        req.header(HEADER_SUBJECT).to_string(),
        req.header(HEADER_COMMAND).to_string(),
    )
}

/// A write over RPC to a protected domain (`protectedError`, ADR 0167).
fn protected_error(name: &str) -> ConnectError {
    ConnectError::failed_precondition(format!(
        "domain {name} is protected: it is written by the local command line only"
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

/// The issues of a domain (`Service.validateDomain`): its own, else the catalogue it would not make with the domains
/// in force (Go's to word).
async fn issues_of(tx: &graph_store::read::Session, d: &graph_catalog::Domain) -> Result<Vec<Issue>, ConnectError> {
    let mut issues = graph_catalog::validate::validate(d);
    if issues.is_empty()
        && let Err(e) = resolves(tx, d.clone()).await?
    {
        let path = if e.starts_with("item type") {
            "itemTypes"
        } else {
            "nodeTypes"
        };
        issues.push(Issue {
            path: path.into(),
            message: e,
        });
    }
    Ok(issues)
}

/// The issues as `def.Issues.Error` writes them.
fn issues_text(issues: &[Issue]) -> String {
    let parts: Vec<String> = issues
        .iter()
        .map(|i| {
            if i.path.is_empty() {
                i.message.clone()
            } else {
                format!("{}: {}", i.path, i.message)
            }
        })
        .collect();
    parts.join("; ")
}

fn issues_pb(issues: Vec<Issue>) -> Vec<pb::Issue> {
    issues
        .into_iter()
        .map(|i| pb::Issue {
            path: i.path,
            message: i.message,
            ..Default::default()
        })
        .collect()
}

/// Whether the domains in force still make a catalogue with this one in place of its domain's (`Service.Types`), else
/// why not, in `typecat.New`'s words.
async fn resolves(
    tx: &graph_store::read::Session,
    d: graph_catalog::def::Domain,
) -> Result<Result<(), String>, ConnectError> {
    let rs = tx.domain_records().await.map_err(connect_error)?;
    let mut ds = in_force(&rs);
    match ds.iter_mut().find(|x| x.name == d.name) {
        Some(x) => *x = d,
        None => ds.push(d),
    }
    Ok(Catalog::of(&ds).map(|_| ()))
}

/// The definition of a version under another number, as Go writes it again: its JSON with the version replaced in
/// place (Go's JSON starts with the name and the version), else read and written again (the `jsonb` of PostgreSQL,
/// which orders the keys itself).
fn renumbered(src: &StoredDomain, to: &str) -> String {
    let head = format!(
        "{{\"name\":{},\"version\":{}",
        jsontext::to_string(&src.name),
        jsontext::to_string(&src.version)
    );
    if let Some(rest) = src.definition_text.strip_prefix(&head) {
        return format!(
            "{{\"name\":{},\"version\":{}{rest}",
            jsontext::to_string(&src.name),
            jsontext::to_string(&to)
        );
    }
    let mut def = src.definition.clone();
    if let Some(o) = def.as_object_mut() {
        o.insert("version".into(), to.into());
    }
    jsontext::to_string(&def)
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

/// An algorithm of the protocol (`domainpb.AlgorithmsFromPB`).
fn algorithm(a: &pb::Algorithm) -> graph_catalog::algo::Algorithm {
    let params = a
        .params
        .iter()
        .map(|p| graph_catalog::algo::Param {
            name: p.name.clone(),
            typ: p.r#type.clone(),
            required: p.required,
            default: p.default_value.as_ref().map(convert::json),
            values: (!p.values.is_empty()).then(|| p.values.clone()),
        })
        .collect::<Vec<_>>();
    graph_catalog::algo::Algorithm {
        name: a.name.clone(),
        typ: a.r#type.clone(),
        language: a.language.clone(),
        code: a.code.clone(),
        params: (!params.is_empty()).then_some(params),
    }
}

/// A node of a sample input (`dsl.Node`).
#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct SampleNode {
    id: Option<String>,
    version: Option<serde_json::Number>,
    key: Option<String>,
    #[serde(rename = "type")]
    typ: Option<String>,
    state: Option<String>,
    props: Option<serde_json::Map<String, serde_json::Value>>,
}

impl SampleNode {
    fn node(self) -> graph_script::js::Node {
        graph_script::js::Node {
            id: self.id.unwrap_or_default(),
            version: self.version.and_then(|n| n.as_f64()).unwrap_or_default() as i64,
            key: self.key.unwrap_or_default(),
            typ: self.typ.unwrap_or_default(),
            state: self.state.unwrap_or_default(),
            props: self.props.unwrap_or_default(),
        }
    }
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct SampleLink {
    #[serde(rename = "type")]
    typ: Option<String>,
    props: Option<serde_json::Map<String, serde_json::Value>>,
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct SampleNamed {
    id: Option<String>,
    name: Option<String>,
    title: Option<String>,
    intent: Option<String>,
    methodology: Option<String>,
    goal: Option<String>,
    from: Option<String>,
    to: Option<String>,
}

/// The sample input of a trial (Go decodes it into the fields of `dsl.AlgorithmInput`).
#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct Sample {
    node: Option<SampleNode>,
    property: Option<String>,
    value: Option<serde_json::Value>,
    link: Option<SampleLink>,
    from: Option<SampleNode>,
    to: Option<SampleNode>,
    children: Option<Vec<SampleNode>>,
    transition: Option<SampleNamed>,
    change: Option<SampleNamed>,
}

impl Sample {
    fn input(self) -> graph_script::js::Input {
        let link = self.link.unwrap_or_default();
        let t = self.transition.unwrap_or_default();
        let c = self.change.unwrap_or_default();
        graph_script::js::Input {
            node: self.node.unwrap_or_default().node(),
            property: self.property.unwrap_or_default(),
            value: self.value.filter(|v| !v.is_null()),
            link_type: link.typ.unwrap_or_default(),
            link_props: link.props.unwrap_or_default(),
            from: self.from.unwrap_or_default().node(),
            to: self.to.unwrap_or_default().node(),
            children: self
                .children
                .unwrap_or_default()
                .into_iter()
                .map(SampleNode::node)
                .collect(),
            change: graph_script::js::ChangeInfo {
                id: c.id.unwrap_or_default(),
                title: c.title.unwrap_or_default(),
                intent: c.intent.unwrap_or_default(),
                methodology: c.methodology.unwrap_or_default(),
                goal: c.goal.unwrap_or_default(),
            },
            transition: [
                t.name.unwrap_or_default(),
                t.from.unwrap_or_default(),
                t.to.unwrap_or_default(),
            ],
            ..graph_script::js::Input::default()
        }
    }
}

/// The `*.yaml` / `*.yml` files of a directory, in name order (Go's `filepath.Glob("*.y*ml")`).
fn yaml_files(dir: &std::path::Path) -> Result<Vec<std::path::PathBuf>, String> {
    let mut files: Vec<_> = std::fs::read_dir(dir)
        .map_err(|e| format!("{}: {e}", dir.display()))?
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| {
            let name = p.file_name().and_then(|n| n.to_str()).unwrap_or_default();
            name.rsplit_once('.')
                .is_some_and(|(_, ext)| ext.starts_with('y') && ext.ends_with("ml"))
        })
        .collect();
    files.sort();
    Ok(files)
}

impl Domains {
    /// Refuses a write over RPC to a protected domain (`Service.writable`, ADR 0167); the local command line and the
    /// seeds write it.
    async fn writable(&self, name: &str, local: bool) -> Result<(), ConnectError> {
        if local {
            return Ok(());
        }
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        if tx.protected_domains().await.map_err(connect_error)?.contains(name) {
            return Err(protected_error(name));
        }
        Ok(())
    }

    /// Protects a domain or lifts its protection (`Service.Protect`): the local command line's, a name with no version
    /// stored yet included.
    pub async fn protect(&self, name: &str, on: bool, by: &str) -> Result<(), String> {
        if name.is_empty() {
            return Err("a domain name is required: invalid domain".into());
        }
        let tx = self.writer().await.map_err(|e| e.message().to_string())?;
        let at = jsontext::time_text(&chrono::Utc::now());
        tx.set_domain_protected(name, on, by, &at)
            .await
            .map_err(|e| e.message)?;
        tx.commit().await.map_err(|e| GraphError::from(e).message)?;
        Ok(())
    }

    /// The stored versions with their status and protection, for the command line: `(name, version, status,
    /// protected)`, by name and creation.
    pub async fn versions(&self) -> Result<Vec<(String, String, String, bool)>, String> {
        let tx = self.state.store.reader().await.map_err(|e| e.to_string())?;
        let mut rs = tx.domain_records().await.map_err(|e| e.message)?;
        rs.sort_by(|a, b| {
            a.domain
                .name
                .cmp(&b.domain.name)
                .then(a.created_at.unwrap_or_default().cmp(&b.created_at.unwrap_or_default()))
        });
        let protected = tx.protected_domains().await.map_err(|e| e.message)?;
        Ok(rs
            .into_iter()
            .map(|r| {
                let p = protected.contains(&r.domain.name);
                (r.domain.name, r.domain.version, r.status, p)
            })
            .collect())
    }

    /// Publishes a domain version from its YAML as the local command line (`ImportDomain` on a local context): a
    /// protected domain included. `None` when the version is stored already and `skip_stored`; a version with issues is
    /// kept as a draft and refused.
    pub async fn publish_local(&self, src: &str, skip_stored: bool, by: &str) -> Result<Option<String>, String> {
        let def = Definition::read(src)?;
        if def.name.is_empty() || def.version.is_empty() {
            return Err("name and version are required: invalid domain".into());
        }
        let tx = self.writer().await.map_err(|e| e.message().to_string())?;
        if skip_stored
            && tx
                .domain_status(&def.name, &def.version)
                .await
                .map_err(|e| e.message)?
                .is_some()
        {
            return Ok(None);
        }
        let json = def.json.as_ref().map_err(|e| e.to_string())?;
        let domain = graph_catalog::Domain::from_json(json)?;
        let issues = issues_of(&tx, &domain).await.map_err(|e| e.message().to_string())?;
        let status = if issues.is_empty() {
            STATUS_PUBLISHED
        } else {
            STATUS_DRAFT
        };
        self.store(tx, &def.name, &def.version, json, status, (by, ""))
            .await
            .map_err(|e| e.message().to_string())?;
        if !issues.is_empty() {
            return Err(format!("invalid domain: cannot publish: {}", issues_text(&issues)));
        }
        Ok(Some(format!("{}@{}", def.name, def.version)))
    }

    /// Publishes the domain versions of the files of a directory not stored yet (Go's `Service.SeedDomains`, ADR
    /// 0113), as the local command line: what it stored, `name@version`, in the order of the file names. A file that
    /// does not read, or a version with issues (kept as a draft), stops it.
    pub async fn seed_domains(&self, dir: &std::path::Path) -> Result<Vec<String>, String> {
        let mut loaded = Vec::new();
        for f in yaml_files(dir)? {
            let shown = f.display().to_string();
            let src = std::fs::read_to_string(&f).map_err(|e| format!("{shown}: {e}"))?;
            if let Some(d) = self
                .publish_local(&src, true, SYSTEM_DOMAINS)
                .await
                .map_err(|e| format!("{shown}: {e}"))?
            {
                loaded.push(d);
            }
        }
        Ok(loaded)
    }

    /// Publishes the built-in domains of a directory whose version is not stored yet, and protects them (Go's
    /// `Service.SeedBuiltinDomains`, ADR 0167). They reference each other, so a file is tried again once another one
    /// landed; with no file found it fails: a registry without them cannot type a graph.
    pub async fn seed_builtin_domains(&self, dir: &std::path::Path) -> Result<Vec<String>, String> {
        let files = yaml_files(dir)?;
        if files.is_empty() {
            return Err(format!(
                "no built-in domain in {} (GOAP_BUILTIN_DOMAINS_DIR): domain not found",
                dir.display()
            ));
        }
        let mut todo = Vec::new();
        for f in files {
            let shown = f.display().to_string();
            let src = std::fs::read_to_string(&f).map_err(|e| format!("{shown}: {e}"))?;
            let def = Definition::read(&src).map_err(|e| format!("{shown}: {e}"))?;
            self.protect(&def.name, true, SYSTEM_DOMAINS).await?;
            let tx = self.state.store.reader().await.map_err(|e| e.to_string())?;
            if tx
                .domain_status(&def.name, &def.version)
                .await
                .map_err(|e| e.message)?
                .is_none()
            {
                todo.push((shown, src, def.name, def.version));
            }
        }
        let mut loaded = Vec::new();
        while !todo.is_empty() {
            let mut left = Vec::new();
            let mut last = String::new();
            let before = todo.len();
            for (shown, src, name, version) in todo {
                match self.publish_local(&src, false, SYSTEM_DOMAINS).await {
                    Ok(d) => loaded.extend(d),
                    Err(e) => {
                        last = format!("{shown}: {e}");
                        // kept as a draft: removed, to be tried again whole
                        let tx = self.writer().await.map_err(|e| e.message().to_string())?;
                        tx.delete_domain_draft(&name, &version, &[])
                            .await
                            .map_err(|e| e.message)?;
                        tx.commit().await.map_err(|e| GraphError::from(e).message)?;
                        left.push((shown, src, name, version));
                    }
                }
            }
            if left.len() == before {
                return Err(last);
            }
            todo = left;
        }
        Ok(loaded)
    }
}

/// The domain registry acting by itself (`authz.System("domains")`): the seeds and the command line.
pub const SYSTEM_DOMAINS: &str = "system:domains";

/// `GOAP_BUILTIN_DOMAINS_DIR`, else `<domains>/builtin` when it is a directory, else `lib/domains/builtin` (Go's
/// `domainsvc.BuiltinDir`).
pub fn builtin_dir(domains: &std::path::Path) -> std::path::PathBuf {
    if let Ok(d) = std::env::var("GOAP_BUILTIN_DOMAINS_DIR")
        && !d.is_empty()
    {
        return d.into();
    }
    let d = domains.join("builtin");
    if d.is_dir() {
        return d;
    }
    std::path::Path::new("lib").join("domains").join("builtin")
}
