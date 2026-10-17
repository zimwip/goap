//! The rules of the domain registry (ADR 0125 §3).

use std::future::Future;
use std::path::Path;
use std::pin::Pin;
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_authz::{self as authz, Authorizer, Resource};
use goap_ctx::Ctx;
use goap_def::{
    self as def, Domain, Issue, Issues, Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED,
};
use goap_typecat::{self as typecat, Catalog};
use serde_json::Value;

use crate::error::{BoxError, Error};

/// Publisher publishes the events of the domains (`goap.registry.domain.saved | published | deleted`, payload `{name,
/// version, status}`).
#[async_trait]
pub trait Publisher: Send + Sync {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError>;
}

#[async_trait]
impl Publisher for goap_platform::Events {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError> {
        goap_platform::Events::publish(self, ctx, subject, v)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }
}

/// The future a [`ChangedFn`] returns.
pub type ChangedFuture = Pin<Box<dyn Future<Output = ()> + Send>>;

/// The hook called after a version is published or archived (Go's `Service.Changed`): it gets a context that is not
/// cancelled with the request's, and the record.
pub type ChangedFn = Arc<dyn Fn(Ctx, Record) -> ChangedFuture + Send + Sync>;

/// Makes a [`ChangedFn`] of an async closure.
pub fn changed_fn<F, Fut>(f: F) -> ChangedFn
where
    F: Fn(Ctx, Record) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = ()> + Send + 'static,
{
    Arc::new(move |ctx, r| Box::pin(f(ctx, r)))
}

/// The clock of a service (tests).
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// Service holds the rules of the domain registry: drafts are editable, published versions are immutable and must be
/// valid, writes are authorized by ABAC rules on the "domain" resource (actions write, publish, delete).
#[derive(Clone)]
pub struct Service {
    pub store: Arc<dyn crate::Store>,
    /// None: every write is allowed.
    pub authz: Option<Arc<dyn Authorizer>>,
    pub events: Option<Arc<dyn Publisher>>,
    /// Changed is called after a version is published or archived, once stored: the types in force may have changed
    /// (the graph in the same process reloads its catalogue). None: nothing is called.
    pub changed: Option<ChangedFn>,
    /// The clock (None: now, UTC).
    pub now: Option<Clock>,
}

impl Service {
    /// A service over a store, with no authorizer, no events and no hook.
    pub fn new(store: Arc<dyn crate::Store>) -> Self {
        Service {
            store,
            authz: None,
            events: None,
            changed: None,
            now: None,
        }
    }

    fn clock(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    /// Types is the type catalogue in force (ADR 0012 §2): the latest published version of every domain and the
    /// built-in domains. `over` replaces the published version of a domain (to check a candidate version).
    pub async fn types(&self, ctx: &Ctx, over: &[&Domain]) -> Result<Catalog, Error> {
        let ds = self.domains(ctx).await?;
        let mut order: Vec<String> = Vec::new();
        let mut by_name: std::collections::HashMap<String, Domain> =
            std::collections::HashMap::new();
        for d in ds.iter().chain(over.iter().copied()) {
            if !by_name.contains_key(&d.name) {
                order.push(d.name.clone());
            }
            by_name.insert(d.name.clone(), d.clone());
        }
        let list: Vec<Domain> = order.iter().filter_map(|n| by_name.remove(n)).collect();
        Ok(typecat::new(&list)?)
    }

    pub(crate) async fn authorize_domain(
        &self,
        ctx: &Ctx,
        action: &str,
        d: &Domain,
    ) -> Result<(), Error> {
        let who = authz::principal_from(ctx);
        let req = authz::Request {
            subject: who.clone(),
            action: action.to_string(),
            resource: Resource {
                r#type: "domain".into(),
                id: format!("{}@{}", d.name, d.version),
                name: d.name.clone(),
                org: who.org.clone(),
                ..Resource::default()
            },
        };
        authz::check(ctx, self.authz.as_deref(), &req).await?;
        Ok(())
    }

    async fn publish_domain_event(&self, ctx: &Ctx, event: &str, r: &Record) {
        if let Some(ev) = &self.events {
            let v = serde_json::json!({
                "name": r.domain.name,
                "version": r.domain.version,
                "status": r.status.as_str(),
            });
            let _ = ev
                .publish(ctx, &format!("goap.registry.domain.{event}"), &v)
                .await;
        }
        if event != "saved"
            && let Some(f) = &self.changed
        {
            f(ctx.without_cancel(), r.clone()).await;
        }
    }

    /// GetDomain returns a version (empty version: latest published), the built-in domains included.
    pub async fn get_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record, Error> {
        if let Some(r) = builtin_domains()
            .into_iter()
            .find(|r| r.domain.name == name && (version.is_empty() || version == r.domain.version))
        {
            return Ok(r);
        }
        self.store.get_domain(ctx, name, version).await
    }

    /// Domains returns the latest published version of every stored domain (the source of the type catalogue, which
    /// adds the built-in ones itself).
    pub async fn domains(&self, ctx: &Ctx) -> Result<Vec<Domain>, Error> {
        let rs = self.domain_versions(ctx, false).await?;
        Ok(def::in_force(&rs))
    }

    /// DomainVersions returns every version, or the latest version of each domain (latest published if any, latest
    /// draft otherwise); the built-in domains come first.
    pub async fn domain_versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<Record>, Error> {
        let stored = self.store.list_domains(ctx).await?;
        let mut rs = builtin_domains();
        rs.extend(stored);
        if all {
            return Ok(rs);
        }
        Ok(def::latest(&rs))
    }

    /// SaveDomain creates or replaces a draft; invalid drafts are stored and their issues returned.
    pub async fn save_domain(&self, ctx: &Ctx, d: Domain) -> Result<(Record, Issues), Error> {
        if d.name.is_empty() || d.version.is_empty() {
            return Err(Error::invalid("name and version are required"));
        }
        err_builtin(&d.name)?;
        self.authorize_domain(ctx, "write", &d).await?;
        let now = self.clock();
        let r = Record {
            domain: d.clone(),
            status: STATUS_DRAFT,
            created_at: Some(now),
            updated_at: Some(now),
            updated_by: authz::principal_from(ctx).subject,
            ..Record::default()
        };
        self.store.save_domain(ctx, r).await?;
        let saved = self.store.get_domain(ctx, &d.name, &d.version).await?;
        self.publish_domain_event(ctx, "saved", &saved).await;
        let issues = self.validate_domain(ctx, &d).await;
        Ok((saved, issues))
    }

    /// validateDomain checks a domain and its references to the types of the other domains in force.
    async fn validate_domain(&self, ctx: &Ctx, d: &Domain) -> Issues {
        let mut issues = d.validate();
        if typecat::is_builtin(&d.name) {
            issues.push(Issue::new(
                "name",
                format!(
                    "{} is a domain built into the platform: it changes with the platform code",
                    d.name
                ),
            ));
            return issues;
        }
        if issues.is_empty()
            && let Err(e) = self.types(ctx, &[d]).await
        {
            let msg = e.to_string();
            let path = if msg.starts_with("item type") {
                "itemTypes"
            } else {
                "nodeTypes"
            };
            issues.push(Issue::new(path, msg));
        }
        issues
    }

    /// PublishDomain freezes a valid draft; it becomes the version in force. The methodologies it breaks are the
    /// methodology registry's to suspend, on the event (ADR 0125 §3): a domain is never refused for them.
    pub async fn publish_domain(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<Record, Error> {
        err_builtin(name)?;
        let r = self.store.get_domain(ctx, name, version).await?;
        self.authorize_domain(ctx, "publish", &r.domain).await?;
        if r.status != STATUS_DRAFT {
            return Err(Error::immutable(format!("domain {name}@{version}")));
        }
        self.check_publishable(ctx, &r.domain).await?;
        self.store
            .set_domain_status(ctx, name, version, STATUS_PUBLISHED, self.clock())
            .await?;
        let r = self.store.get_domain(ctx, name, version).await?;
        self.publish_domain_event(ctx, "published", &r).await;
        Ok(r)
    }

    /// checkPublishable refuses a domain with issues, or one whose types do not resolve with the other domains in
    /// force.
    async fn check_publishable(&self, ctx: &Ctx, d: &Domain) -> Result<(), Error> {
        let issues = self.validate_domain(ctx, d).await;
        if !issues.is_empty() {
            return Err(Error::invalid_because(issues));
        }
        if let Err(e) = self.types(ctx, &[d]).await {
            return Err(Error::invalid_because(e));
        }
        Ok(())
    }

    /// CreateDomainVersion copies a version into a new draft.
    pub async fn create_domain_version(
        &self,
        ctx: &Ctx,
        name: &str,
        from: &str,
        to: &str,
    ) -> Result<Record, Error> {
        err_builtin(name)?;
        let src = self.get_domain(ctx, name, from).await?;
        if to.is_empty() || to == src.domain.version {
            return Err(Error::invalid("a new version number is required"));
        }
        if self.store.get_domain(ctx, name, to).await.is_ok() {
            return Err(Error::immutable(format!(
                "domain {name}@{to} already exists"
            )));
        }
        let mut d = src.domain;
        d.version = to.to_string();
        let (r, _) = self.save_domain(ctx, d).await?;
        Ok(r)
    }

    /// DeleteDomain removes a draft, or archives a published version (the methodologies the archive breaks are
    /// suspended by the methodology registry, on the event).
    pub async fn delete_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<(), Error> {
        err_builtin(name)?;
        let r = self.store.get_domain(ctx, name, version).await?;
        self.authorize_domain(ctx, "delete", &r.domain).await?;
        if r.status == STATUS_ARCHIVED {
            return Ok(());
        }
        if r.status == STATUS_DRAFT {
            self.store.delete_domain(ctx, name, version).await?;
        } else {
            self.store
                .set_domain_status(ctx, name, version, STATUS_ARCHIVED, self.clock())
                .await?;
        }
        self.publish_domain_event(ctx, "deleted", &r).await;
        Ok(())
    }

    /// ImportDomain stores a YAML definition as a draft, and publishes it on request.
    pub async fn import_domain(
        &self,
        ctx: &Ctx,
        yaml_src: &[u8],
        publish: bool,
    ) -> Result<(Record, Issues), Error> {
        let d = def::parse_domain(yaml_src).map_err(Error::invalid_because)?;
        if !publish {
            return self.save_domain(ctx, d).await;
        }
        self.import_published_domain(ctx, d).await
    }

    /// importPublishedDomain stores a domain version as published in one write, with one event (SaveDomain then
    /// PublishDomain would be two). A domain with issues cannot be published: it is kept as a draft, and the issues
    /// returned with the error (`ImportError`).
    async fn import_published_domain(
        &self,
        ctx: &Ctx,
        d: Domain,
    ) -> Result<(Record, Issues), Error> {
        if d.name.is_empty() || d.version.is_empty() {
            return Err(Error::invalid("name and version are required"));
        }
        err_builtin(&d.name)?;
        self.authorize_domain(ctx, "write", &d).await?;
        self.authorize_domain(ctx, "publish", &d).await?;
        let issues = self.validate_domain(ctx, &d).await;
        if !issues.is_empty() {
            let (r, _) = self.save_domain(ctx, d).await?;
            return Err(Error::Draft {
                record: Box::new(r),
                issues: issues.clone(),
                message: format!("{}: cannot publish: {issues}", crate::ERR_INVALID),
            });
        }
        self.check_publishable(ctx, &d).await?;
        let now = self.clock();
        let r = Record {
            domain: d.clone(),
            status: STATUS_PUBLISHED,
            created_at: Some(now),
            updated_at: Some(now),
            published_at: Some(now),
            updated_by: authz::principal_from(ctx).subject,
            builtin: false,
        };
        self.store.save_domain(ctx, r).await?;
        let saved = self.store.get_domain(ctx, &d.name, &d.version).await?;
        self.publish_domain_event(ctx, "published", &saved).await;
        Ok((saved, Issues::default()))
    }

    /// ExportDomain renders a version as YAML, or as JSON with format "json" (the same document: ImportDomain reads
    /// both). It returns the bytes and the file name.
    pub async fn export_domain(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
        format: &str,
    ) -> Result<(Vec<u8>, String), Error> {
        let r = self.get_domain(ctx, name, version).await?;
        let out = r.domain.yaml()?;
        let base = format!("domain-{}-{}", r.domain.name, r.domain.version);
        match format {
            "" | "yaml" => Ok((out.into_bytes(), format!("{base}.yaml"))),
            "json" => {
                // through the YAML document, so that the JSON carries the same names and omissions
                let doc: Value =
                    serde_yaml_ng::from_str(&out).map_err(|e| Error::Other(e.to_string()))?;
                let js =
                    serde_json::to_string_pretty(&doc).map_err(|e| Error::Other(e.to_string()))?;
                Ok((go_escape(&js).into_bytes(), format!("{base}.json")))
            }
            _ => Err(Error::invalid(format!(
                "unknown export format {format:?} (yaml or json)"
            ))),
        }
    }

    /// SeedDomains imports and publishes the YAML files of dir whose domain version is not stored yet (dev bootstrap,
    /// before the methodologies that reference them). It runs with the given principal. It returns the versions it
    /// loaded (`name@version`); on an error, those loaded before it are in [`SeedError`].
    pub async fn seed_domains(
        &self,
        ctx: &Ctx,
        dir: impl AsRef<Path>,
    ) -> Result<Vec<String>, SeedError> {
        let dir = dir.as_ref();
        let mut loaded = Vec::new();
        let files = match seed_files(dir) {
            Ok(f) => f,
            Err(e) => return Err(SeedError { loaded, error: e }),
        };
        for f in files {
            let shown = f.display().to_string();
            let src = match std::fs::read(&f) {
                Ok(s) => s,
                Err(e) => {
                    return Err(SeedError {
                        loaded,
                        error: e.into(),
                    });
                }
            };
            let d = match def::parse_domain(&src) {
                Ok(d) => d,
                Err(e) => {
                    return Err(SeedError {
                        loaded,
                        error: Error::Other(format!("{shown}: {e}")),
                    });
                }
            };
            if self
                .store
                .get_domain(ctx, &d.name, &d.version)
                .await
                .is_ok()
            {
                continue;
            }
            if let Err(e) = self.import_domain(ctx, &src, true).await {
                return Err(SeedError {
                    loaded,
                    error: e.context(&shown),
                });
            }
            loaded.push(format!("{}@{}", d.name, d.version));
        }
        Ok(loaded)
    }
}

/// The error of [`Service::seed_domains`], with the versions loaded before it (Go returns both).
#[derive(Debug, thiserror::Error)]
#[error("{error}")]
pub struct SeedError {
    pub loaded: Vec<String>,
    #[source]
    pub error: Error,
}

/// The files of dir matching `*.y*ml`, in name order (`filepath.Glob`).
fn seed_files(dir: &Path) -> Result<Vec<std::path::PathBuf>, Error> {
    let rd = match std::fs::read_dir(dir) {
        Ok(rd) => rd,
        // Glob of a missing directory matches nothing
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e.into()),
    };
    let mut out: Vec<std::path::PathBuf> = Vec::new();
    for e in rd {
        let e = e?;
        let name = e.file_name().to_string_lossy().to_string();
        if glob_yaml(&name) {
            out.push(dir.join(name));
        }
    }
    out.sort();
    Ok(out)
}

/// Matches `*.y*ml`.
fn glob_yaml(name: &str) -> bool {
    name.match_indices(".y")
        .any(|(i, _)| name[i + 2..].ends_with("ml"))
}

/// Escapes `<`, `>`, `&`, U+2028 and U+2029 the way Go's encoding/json does (they appear in strings only).
fn go_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// builtinDomains are the records of the domains shipped with the platform: published, frozen, never stored.
fn builtin_domains() -> Vec<Record> {
    typecat::builtins()
        .iter()
        .map(|d| Record {
            domain: d.clone(),
            status: STATUS_PUBLISHED,
            updated_by: "platform".into(),
            builtin: true,
            ..Record::default()
        })
        .collect()
}

/// errBuiltin refuses a write to a built-in domain; it is an `Error::Immutable`.
fn err_builtin(name: &str) -> Result<(), Error> {
    if typecat::is_builtin(name) {
        return Err(Error::Immutable(format!(
            "domain {name} is built into the platform: it changes with the platform code"
        )));
    }
    Ok(())
}
