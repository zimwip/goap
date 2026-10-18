//! The rules of the methodology registry.

use std::collections::{BTreeMap, HashSet};
use std::future::Future;
use std::path::Path;
use std::pin::Pin;
use std::sync::Arc;

use chrono::{DateTime, Utc};
use goap_authz::{self as authz, Authorizer, Resource};
use goap_ctx::Ctx;
use goap_def::{Issue, Issues, TypeSet};
use goap_engine::{Payload, Publisher};
use goap_methodology::{Compiled, Methodology};

use crate::compilecache::CompileCache;
use crate::domains::Domains;
use crate::error::{BoxError, Error, Result};
use crate::store::{Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, Store, after, key};

/// The future of an async callback of the service.
pub type BoxFuture<T> = Pin<Box<dyn Future<Output = T> + Send>>;

/// MCPScopes gives the scope of each MCP of the platform (ADR 0028), by MCP name.
pub type McpScopesFn = Arc<
    dyn Fn(Ctx) -> BoxFuture<std::result::Result<BTreeMap<String, String>, BoxError>> + Send + Sync,
>;

/// Applicable lists the methodologies applicable to a project, nearest project of its chain first (ADR 0039).
pub type ApplicableFn =
    Arc<dyn Fn(Ctx, String) -> BoxFuture<std::result::Result<Vec<String>, BoxError>> + Send + Sync>;

/// The clock of a service (tests).
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// Makes an [`McpScopesFn`] of an async closure.
pub fn mcp_scopes_fn<F, Fut>(f: F) -> McpScopesFn
where
    F: Fn(Ctx) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = std::result::Result<BTreeMap<String, String>, BoxError>> + Send + 'static,
{
    Arc::new(move |ctx| Box::pin(f(ctx)))
}

/// Makes an [`ApplicableFn`] of an async closure.
pub fn applicable_fn<F, Fut>(f: F) -> ApplicableFn
where
    F: Fn(Ctx, String) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = std::result::Result<Vec<String>, BoxError>> + Send + 'static,
{
    Arc::new(move |ctx, project| Box::pin(f(ctx, project)))
}

/// Service holds the registry rules: drafts are editable, published versions are immutable and must be valid, writes
/// are authorized by ABAC rules on the "methodology" resource (actions write, publish, delete).
pub struct Service {
    /// Store holds the methodology versions (graph data, GraphStore).
    pub store: Arc<dyn Store>,
    /// Domains reads the domain registry (ADR 0125 §3): a newtype over `goap_domainsvc::Service` in process,
    /// `goap_domainclient::Client` over RPC. None: the built-in domains only.
    pub domains: Option<Arc<dyn Domains>>,
    /// None: every write is allowed.
    pub authz: Option<Arc<dyn Authorizer>>,
    pub events: Option<Arc<dyn Publisher>>,
    /// MCPScopes gives the scope of each MCP of the platform (ADR 0028), checked against where a methodology
    /// declares them; None: not checked.
    pub mcp_scopes: Option<McpScopesFn>,
    /// Applicable lists the methodologies applicable to a project, nearest project of its chain first (ADR 0039):
    /// where the guardian chain of a change looks for the methodologies integrating its own (ADR 0106). None: a
    /// change's chain is its own methodology.
    pub applicable: Option<ApplicableFn>,
    /// The clock (None: now, UTC).
    pub now: Option<Clock>,
    /// compiled caches the compiled methodologies (ADR 0072).
    pub(crate) compiled: CompileCache,
    /// aliasMu serializes ensure_alias_stubs (check, then create).
    pub(crate) alias_mu: tokio::sync::Mutex<()>,
    /// suspended is what Recheck last found suspended, by name@version: the reason of each (ADR 0125 §3).
    pub(crate) suspended: parking_lot::Mutex<BTreeMap<String, String>>,
}

impl Service {
    /// A service over a store, with no domain registry, no authorizer, no events and no hook.
    pub fn new(store: Arc<dyn Store>) -> Service {
        Service {
            store,
            domains: None,
            authz: None,
            events: None,
            mcp_scopes: None,
            applicable: None,
            now: None,
            compiled: CompileCache::default(),
            alias_mu: tokio::sync::Mutex::new(()),
            suspended: parking_lot::Mutex::new(BTreeMap::new()),
        }
    }

    pub(crate) fn clock(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    async fn authorize(&self, ctx: &Ctx, action: &str, m: &Methodology) -> Result<()> {
        let who = authz::principal_from(ctx);
        let req = authz::Request {
            subject: who.clone(),
            action: action.to_string(),
            resource: Resource {
                r#type: "methodology".into(),
                id: key(&m.name, &m.version),
                name: m.name.clone(),
                org: who.org.clone(),
                ..Resource::default()
            },
        };
        authz::check(ctx, self.authz.as_deref(), &req).await?;
        Ok(())
    }

    async fn publish(&self, ctx: &Ctx, event: &str, r: &Record) {
        self.compiled.clear(); // every write of a methodology version goes through here
        if let Some(ev) = &self.events {
            let v: BTreeMap<String, String> = BTreeMap::from([
                ("name".to_string(), r.methodology.name.clone()),
                ("version".to_string(), r.methodology.version.clone()),
                ("status".to_string(), r.status.to_string()),
            ]);
            let _ = ev
                .publish(
                    ctx,
                    &format!("goap.registry.methodology.{event}"),
                    &Payload::new(&v),
                )
                .await;
        }
    }

    /// Get returns a version (empty version: latest published).
    pub async fn get(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record> {
        self.store.get(ctx, name, version).await
    }

    /// Versions returns every version, or the latest version of each methodology (latest published if any, latest
    /// draft otherwise).
    pub async fn versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<Record>> {
        let rs = self.store.list(ctx).await?;
        if all {
            return Ok(rs);
        }
        let mut latest: BTreeMap<String, Record> = BTreeMap::new();
        let mut order: Vec<String> = Vec::new();
        for r in rs {
            let name = r.methodology.name.clone();
            let cur = latest.get(&name);
            if cur.is_none() {
                order.push(name.clone());
            }
            let better = match cur {
                None => true,
                Some(cur) => {
                    (r.status == STATUS_PUBLISHED
                        && (cur.status != STATUS_PUBLISHED
                            || after(&r.published_at, &cur.published_at)))
                        || (cur.status != STATUS_PUBLISHED
                            && r.status != STATUS_ARCHIVED
                            && after(&r.created_at, &cur.created_at))
                }
            };
            if better {
                latest.insert(name, r);
            }
        }
        Ok(order.iter().filter_map(|n| latest.remove(n)).collect())
    }

    /// Save creates or replaces a draft; invalid drafts are stored and their issues returned.
    pub async fn save(&self, ctx: &Ctx, m: Methodology) -> Result<(Record, Issues)> {
        if m.name.is_empty() || m.version.is_empty() {
            return Err(Error::invalid("name and version are required"));
        }
        self.authorize(ctx, "write", &m).await?;
        let now = self.clock();
        let r = Record {
            methodology: m.clone(),
            status: STATUS_DRAFT,
            created_at: Some(now),
            updated_at: Some(now),
            published_at: None,
            updated_by: authz::principal_from(ctx).subject,
        };
        self.store.save(ctx, r).await?;
        let saved = self.store.get(ctx, &m.name, &m.version).await?;
        self.publish(ctx, "saved", &saved).await;
        let issues = self.validate(ctx, &m).await;
        Ok((saved, issues))
    }

    /// validate checks a definition against the types in force (the published domains, ADR 0012).
    pub async fn validate(&self, ctx: &Ctx, m: &Methodology) -> Issues {
        let issues = self.domain_pin_issues(ctx, m).await;
        if !issues.0.is_empty() {
            return issues;
        }
        let cat = match self.types_for(ctx, m).await {
            Ok((cat, _)) => cat,
            Err(e) => {
                return Issues(vec![Issue::new(
                    "namespace",
                    format!("the types in force cannot be read: {e}"),
                )]);
            }
        };
        let cat: Arc<goap_typecat::Catalog> = Arc::new(cat);
        let mut issues = resolve(m, cat.clone()).validate_stored();
        if !m.namespace.is_empty() && !cat.domains().contains_key(&m.namespace) {
            issues.0.push(Issue::new(
                "namespace",
                format!("no published domain {}", m.namespace),
            ));
        }
        issues.0.extend(self.integration_issues(ctx, m).await.0);
        issues.0.extend(self.composition_issues(ctx, m).await.0);
        issues.0.extend(self.scope_issues(ctx, m).await.0);
        issues
    }

    /// Publish freezes a valid draft.
    pub async fn publish_version(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record> {
        let mut r = self.store.get(ctx, name, version).await?;
        self.authorize(ctx, "publish", &r.methodology).await?;
        if r.status != STATUS_DRAFT {
            return Err(Error::immutable(key(name, version)));
        }
        let issues = self.validate(ctx, &r.methodology).await;
        if issues.has_errors() {
            return Err(Error::invalid_because(issues.errors()));
        }
        // the published version keeps the domain versions it was validated against (ADR 0113)
        let before = r.methodology.domains.len();
        self.pin_domains(ctx, &mut r.methodology).await?;
        if r.methodology.domains.len() != before {
            self.store.save(ctx, r.clone()).await?;
        }
        self.store
            .set_status(ctx, name, version, STATUS_PUBLISHED, self.clock())
            .await?;
        let r = self.store.get(ctx, name, version).await?;
        self.ensure_alias_stubs(ctx, &r.methodology).await;
        self.publish(ctx, "published", &r).await;
        Ok(r)
    }

    /// CreateVersion copies a version into a new draft, which follows the latest published domains (its source's
    /// pins are dropped, ADR 0113).
    pub async fn create_version(
        &self,
        ctx: &Ctx,
        name: &str,
        from: &str,
        to: &str,
    ) -> Result<Record> {
        let src = self.store.get(ctx, name, from).await?;
        if to.is_empty() || to == src.methodology.version {
            return Err(Error::invalid("a new version number is required"));
        }
        if self.store.get(ctx, name, to).await.is_ok() {
            return Err(Error::immutable(format!("{name}@{to} already exists")));
        }
        let mut m = src.methodology;
        m.version = to.to_string();
        // a new version moves to the latest domains (ADR 0113): it pins them again when it is published
        m.domains = BTreeMap::new();
        let (r, _) = self.save(ctx, m).await?;
        Ok(r)
    }

    /// Delete removes a draft or archives a published version.
    pub async fn delete(&self, ctx: &Ctx, name: &str, version: &str) -> Result<()> {
        let r = self.store.get(ctx, name, version).await?;
        self.authorize(ctx, "delete", &r.methodology).await?;
        if r.status == STATUS_DRAFT {
            self.store.delete(ctx, name, version).await?;
        } else if r.status == STATUS_PUBLISHED {
            self.store
                .set_status(ctx, name, version, STATUS_ARCHIVED, self.clock())
                .await?;
        } else {
            return Ok(());
        }
        self.publish(ctx, "deleted", &r).await;
        Ok(())
    }

    /// Import stores a YAML definition as a draft, and publishes it on request.
    pub async fn import(
        &self,
        ctx: &Ctx,
        yaml_src: &[u8],
        publish: bool,
    ) -> Result<(Record, Issues)> {
        let m = Methodology::parse(yaml_src).map_err(Error::invalid_because)?;
        if !publish {
            return self.save(ctx, m).await;
        }
        self.import_published(ctx, m).await
    }

    /// importPublished stores a methodology version as published in one change (Save then Publish would be two). A
    /// definition with issues cannot be published: it is kept as a draft, and the issues come with the error
    /// ([`Error::Draft`] in Rust: the draft record and the issues inside it).
    pub async fn import_published(
        &self,
        ctx: &Ctx,
        mut m: Methodology,
    ) -> Result<(Record, Issues)> {
        if m.name.is_empty() || m.version.is_empty() {
            return Err(Error::invalid("name and version are required"));
        }
        self.authorize(ctx, "write", &m).await?;
        self.authorize(ctx, "publish", &m).await?;
        let issues = self.validate(ctx, &m).await;
        if issues.has_errors() {
            let (r, _) = self.save(ctx, m).await?;
            let message = format!(
                "{}: cannot publish: {}",
                crate::ERR_INVALID,
                issues.errors()
            );
            return Err(Error::Draft {
                record: Box::new(r),
                issues,
                message,
            });
        }
        self.pin_domains(ctx, &mut m).await?;
        let now = self.clock();
        let r = Record {
            methodology: m.clone(),
            status: STATUS_PUBLISHED,
            created_at: Some(now),
            updated_at: Some(now),
            published_at: Some(now),
            updated_by: authz::principal_from(ctx).subject,
        };
        self.store.save(ctx, r).await?;
        let saved = self.store.get(ctx, &m.name, &m.version).await?;
        self.ensure_alias_stubs(ctx, &saved.methodology).await;
        self.publish(ctx, "published", &saved).await;
        Ok((saved, Issues::default()))
    }

    /// Export renders a version as YAML: the bytes and the file name.
    pub async fn export(&self, ctx: &Ctx, name: &str, version: &str) -> Result<(Vec<u8>, String)> {
        let r = self.store.get(ctx, name, version).await?;
        let out = r
            .methodology
            .yaml()
            .map_err(|e| Error::Other(e.to_string()))?;
        Ok((
            out.into_bytes(),
            format!("{}-{}.yaml", r.methodology.name, r.methodology.version),
        ))
    }

    /// Seed imports and publishes the YAML files of dir whose version is not stored yet (dev bootstrap). It runs with
    /// the given principal. It returns the versions loaded (`name@version`); on an error, those loaded before it are
    /// in [`SeedError`].
    pub async fn seed(
        &self,
        ctx: &Ctx,
        dir: impl AsRef<Path>,
    ) -> std::result::Result<Vec<String>, SeedError> {
        let mut loaded = Vec::new();
        let files = match seed_files(dir.as_ref()) {
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
            let m = match Methodology::parse(&src) {
                Ok(m) => m,
                Err(e) => {
                    return Err(SeedError {
                        loaded,
                        error: Error::Other(format!("{shown}: {e}")),
                    });
                }
            };
            if self.store.get(ctx, &m.name, &m.version).await.is_ok() {
                continue;
            }
            if let Err(e) = self.import(ctx, &src, true).await {
                return Err(SeedError {
                    loaded,
                    error: e.context(&shown),
                });
            }
            loaded.push(key(&m.name, &m.version));
        }
        Ok(loaded)
    }

    /// List implements engine.MethodologyPort: the latest published version of every methodology, the suspended
    /// ones left out.
    pub async fn list(&self, ctx: &Ctx) -> Result<Vec<Arc<Compiled>>> {
        let rs = self.store.list(ctx).await?;
        let mut seen: HashSet<String> = HashSet::new();
        let mut out = Vec::new();
        for r in rs {
            if r.status != STATUS_PUBLISHED || seen.contains(&r.methodology.name) {
                continue;
            }
            seen.insert(r.methodology.name.clone());
            match self.methodology(ctx, &r.methodology.name).await {
                Err(e) if e.is_suspended() => continue, // not in force (ADR 0125 §3)
                Err(e) => return Err(e),
                Ok(c) => out.push(c),
            }
        }
        Ok(out)
    }
}

/// The error of [`Service::seed`], with the versions loaded before it (Go returns both).
#[derive(Debug, thiserror::Error)]
#[error("{error}")]
pub struct SeedError {
    pub loaded: Vec<String>,
    #[source]
    pub error: Error,
}

/// The files of dir matching `*.y*ml`, in name order (`filepath.Glob`).
fn seed_files(dir: &Path) -> Result<Vec<std::path::PathBuf>> {
    let rd = match std::fs::read_dir(dir) {
        Ok(rd) => rd,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e.into()),
    };
    let mut out = Vec::new();
    for e in rd {
        let p = e?.path();
        let Some(name) = p.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        if name
            .match_indices(".y")
            .any(|(i, _)| name[i + 2..].ends_with("ml"))
        {
            out.push(p);
        }
    }
    out.sort();
    Ok(out)
}

/// resolve gives a definition the types in force and the builtins of the platform, so that a typo in `builtin:` is
/// refused when the definition is validated or published, without an engine (ADR 0062).
pub(crate) fn resolve(m: &Methodology, types: Arc<dyn TypeSet>) -> Methodology {
    m.resolve(types)
        .with_builtins(Arc::new(goap_builtins::Known))
}
