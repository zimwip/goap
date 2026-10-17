//! Package domainclient reads the domain registry (registry.v1.DomainService, served by the graph) over Connect: the
//! client the engine, the methodology registry and the MCP hub share (ADR 0125 §3).

use std::sync::Arc;
use std::time::{Duration, Instant};

use goap_connect::{ConnectError, Request};
use goap_ctx::Ctx;
use goap_def as def;
use goap_domainpb as domainpb;
use goap_gen::registry::v1::{self as pb, DomainService, DomainServiceClient};
use goap_pbconv as pbconv;

/// The text of ErrNotFound: an unknown domain version.
pub const ERR_NOT_FOUND: &str = "domain not found";

/// DefaultTTL is how long a Client keeps the domains it read when nothing invalidates them.
pub const DEFAULT_TTL: Duration = Duration::from_secs(30);

/// The errors of the client.
#[derive(Debug, Clone, thiserror::Error)]
pub enum Error {
    /// An unknown domain version: `<name>@<version>: domain not found`.
    #[error("{0}: domain not found")]
    NotFound(String),
    /// The registry refused or could not be reached.
    #[error(transparent)]
    Rpc(#[from] ConnectError),
}

impl Error {
    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }
}

#[derive(Default)]
struct Cache {
    /// every version, the built-in domains first; None: to read
    all: Option<Vec<def::Record>>,
    loaded: Option<Instant>,
}

/// Client reads the domain registry over Connect: the source of a typecat::Live catalogue (domains) and the domains a
/// methodology registry compiles against (domain_versions, get_domain). A published version is immutable and the
/// domains are few, so it keeps every version it read, until invalidate (on a domain event) or for its TTL.
pub struct Client {
    rpc: Arc<dyn DomainService>,
    /// TTL bounds how long the versions read are kept (DEFAULT_TTL when zero).
    pub ttl: Duration,
    cache: parking_lot::Mutex<Cache>,
    /// serializes the reads of the registry
    load: tokio::sync::Mutex<()>,
}

impl Client {
    /// NewClient returns a domain registry client over a Connect client (its base URL and settings).
    pub fn new(client: goap_connect::Client) -> Self {
        Self::with_service(Arc::new(DomainServiceClient::new(client)))
    }

    /// A client of the registry at `base_url`, default settings.
    pub fn connect(base_url: &str) -> Self {
        Self::new(goap_connect::Client::new(base_url))
    }

    /// A client over any implementation of the service (a test, the service in process).
    pub fn with_service(rpc: Arc<dyn DomainService>) -> Self {
        Client {
            rpc,
            ttl: Duration::ZERO,
            cache: parking_lot::Mutex::new(Cache::default()),
            load: tokio::sync::Mutex::new(()),
        }
    }

    /// Sets the TTL.
    pub fn with_ttl(mut self, ttl: Duration) -> Self {
        self.ttl = ttl;
        self
    }

    /// Invalidate drops what the client read: the next call reads the registry again.
    pub fn invalidate(&self) {
        self.cache.lock().all = None;
    }

    fn cached(&self, ttl: Duration) -> Option<Vec<def::Record>> {
        let c = self.cache.lock();
        match (&c.all, c.loaded) {
            (Some(all), Some(at)) if at.elapsed() < ttl => Some(all.clone()),
            _ => None,
        }
    }

    /// versions returns every version of every domain, read once per TTL.
    async fn versions(&self, ctx: &Ctx) -> Result<Vec<def::Record>, Error> {
        let ttl = if self.ttl.is_zero() {
            DEFAULT_TTL
        } else {
            self.ttl
        };
        if let Some(all) = self.cached(ttl) {
            return Ok(all);
        }
        let _guard = self.load.lock().await;
        if let Some(all) = self.cached(ttl) {
            return Ok(all);
        }
        let list = self
            .rpc
            .list_domains(
                ctx,
                Request::new(pb::ListDomainsRequest { all_versions: true }),
            )
            .await?
            .into_inner();
        let mut all = Vec::with_capacity(list.domains.len());
        for s in &list.domains {
            let d = self
                .rpc
                .get_domain(
                    ctx,
                    Request::new(pb::GetDomainRequest {
                        name: s.name.clone(),
                        version: s.version.clone(),
                    }),
                )
                .await?
                .into_inner();
            let p = d.domain.unwrap_or_default();
            all.push(def::Record {
                domain: domainpb::domain_from_pb(Some(&p)),
                status: def::Status::from(p.status.clone()),
                created_at: pbconv::from_opt_time(p.created_at.as_ref()),
                updated_at: pbconv::from_opt_time(p.updated_at.as_ref()),
                published_at: pbconv::from_opt_time(p.published_at.as_ref()),
                updated_by: p.updated_by.clone(),
                builtin: p.builtin,
            });
        }
        let mut c = self.cache.lock();
        c.all = Some(all.clone());
        c.loaded = Some(Instant::now());
        Ok(all)
    }

    /// Domains returns the latest published version of every stored domain (the source of a typecat::Live
    /// catalogue, which adds the built-in domains itself).
    pub async fn domains(&self, ctx: &Ctx) -> Result<Vec<def::Domain>, Error> {
        Ok(def::in_force(&self.versions(ctx).await?))
    }

    /// DomainVersions returns every version, or the latest version of each domain; the built-in domains come first.
    pub async fn domain_versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<def::Record>, Error> {
        let rs = self.versions(ctx).await?;
        if all {
            return Ok(rs);
        }
        Ok(def::latest(&rs))
    }

    /// GetDomain returns a version (empty version: the latest published one).
    pub async fn get_domain(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<def::Record, Error> {
        let rs = self.versions(ctx).await?;
        let mut best: Option<def::Record> = None;
        for r in rs {
            if r.domain.name != name {
                continue;
            }
            if !version.is_empty() {
                if r.domain.version == version {
                    return Ok(r);
                }
                continue;
            }
            if r.status == def::STATUS_PUBLISHED
                && best
                    .as_ref()
                    .is_none_or(|b| after(&r.published_at, &b.published_at))
            {
                best = Some(r);
            }
        }
        best.ok_or_else(|| Error::NotFound(format!("{name}@{version}")))
    }
}

/// `t.After(u)` with None as Go's zero time.
fn after(
    t: &Option<chrono::DateTime<chrono::Utc>>,
    u: &Option<chrono::DateTime<chrono::Utc>>,
) -> bool {
    match (t, u) {
        (Some(t), Some(u)) => t > u,
        (Some(_), None) => true,
        _ => false,
    }
}

#[goap_connect::async_trait]
impl goap_typecat::Source for Client {
    async fn domains(&self, ctx: &Ctx) -> Result<Vec<def::Domain>, goap_typecat::BoxError> {
        Client::domains(self, ctx).await.map_err(Into::into)
    }
}
