//! Versions (ADR 0113): a change follows a version of its methodology
//! (Change.methodology_version), a methodology version is written against versions of the domains
//! (Methodology.domains). The readers of a methodology take a reference, "<name>@<version>" (domain::methodology_ref),
//! a bare name being the latest published version.

use std::collections::BTreeMap;

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_def::{Domain, Issue, Issues};
use goap_domain::split_methodology_ref;
use goap_graphapi::{Error as GraphError, ErrorKind};
use goap_methodology::Methodology;
use goap_typecat::Catalog;

use crate::error::{Error, Result, SuspendedError};
use crate::service::Service;
use crate::store::{Record, STATUS_DRAFT, key};

impl Service {
    /// record is the stored version a reference names: the version it names, else the latest published one.
    pub(crate) async fn record(&self, ctx: &Ctx, r#ref: &str) -> Result<Record> {
        let (name, version) = split_methodology_ref(r#ref);
        self.store.get(ctx, name, version).await
    }

    /// MethodologyVersion implements graph.ChangeLifecycles: the version a change of a methodology follows. A
    /// reference naming a version gives it when it is published or archived (a draft is refused: graphapi.ErrInvalid);
    /// a bare name gives the latest published version; "" for a methodology the registry does not know (its changes
    /// follow no version).
    pub async fn methodology_version(&self, ctx: &Ctx, r#ref: &str) -> Result<String> {
        let (name, version) = split_methodology_ref(r#ref);
        let r = match self.store.get(ctx, name, version).await {
            Err(e) if e.is_not_found() && version.is_empty() => return Ok(String::new()),
            Err(e) if e.is_not_found() => {
                return Err(Error::Graph(GraphError::wrap(
                    ErrorKind::Invalid,
                    format!("methodology {name} has no version {version}"),
                )));
            }
            Err(e) => return Err(e),
            Ok(r) => r,
        };
        if r.status == STATUS_DRAFT {
            return Err(Error::Graph(GraphError::wrap(
                ErrorKind::Invalid,
                format!("{name}@{version} is a draft: a change follows a published version"),
            )));
        }
        // a suspended version starts no change (ADR 0125 §3)
        let why = self.suspended(ctx, &r).await;
        if !why.is_empty() {
            return Err(Error::Suspended(SuspendedError {
                r#ref: key(&r.methodology.name, &r.methodology.version),
                cause: why,
                graph_invalid: true,
            }));
        }
        Ok(r.methodology.version)
    }

    /// domainsFor are the domains a methodology version is validated and compiled against: the latest published
    /// version of every stored domain, replaced by the version the methodology pins (a pinned version since archived
    /// still serves it). A pin naming a version that does not exist, or a draft, is an error. A version published
    /// before the pins existed (published_at set, a domain it does not pin) follows the version of that domain in
    /// force when it was published, when there was one: what it was validated against.
    pub(crate) async fn domains_for(
        &self,
        ctx: &Ctx,
        m: &Methodology,
        published_at: Option<DateTime<Utc>>,
    ) -> Result<Vec<Domain>> {
        let ds = self.domains_in_force(ctx).await?;
        self.domains_among(ctx, m, published_at, ds).await
    }

    /// domainsAmong is domainsFor given the domains in force.
    pub(crate) async fn domains_among(
        &self,
        ctx: &Ctx,
        m: &Methodology,
        published_at: Option<DateTime<Utc>>,
        ds: Vec<Domain>,
    ) -> Result<Vec<Domain>> {
        let mut then: BTreeMap<String, Domain> = BTreeMap::new();
        if let Some(at) = published_at
            && m.domains.len() < ds.len()
        {
            then = self.domains_in_force_at(ctx, at).await?;
        }
        if m.domains.is_empty() && then.is_empty() {
            return Ok(ds);
        }
        let mut out: Vec<Domain> = Vec::with_capacity(ds.len() + m.domains.len());
        let mut seen = std::collections::HashSet::new();
        for d in ds {
            seen.insert(d.name.clone());
            let pin = m.domains.get(&d.name);
            if let Some(v) = pin
                && *v != d.version
            {
                out.push(self.pinned_domain(ctx, &d.name, v).await?);
                continue;
            }
            if pin.is_none()
                && let Some(old) = then.get(&d.name)
            {
                out.push(old.clone());
                continue;
            }
            out.push(d);
        }
        for (name, v) in &m.domains {
            if seen.contains(name) || goap_typecat::is_builtin(name) {
                continue;
            }
            out.push(self.pinned_domain(ctx, name, v).await?);
        }
        Ok(out)
    }

    /// domainsInForceAt are the versions of the stored domains in force at a time: the one published last before it
    /// (archived since or not), by name; a domain first published after it is absent.
    async fn domains_in_force_at(
        &self,
        ctx: &Ctx,
        at: DateTime<Utc>,
    ) -> Result<BTreeMap<String, Domain>> {
        let rs = self.domain_versions(ctx, true).await?;
        let mut out: BTreeMap<String, Domain> = BTreeMap::new();
        let mut when: BTreeMap<String, DateTime<Utc>> = BTreeMap::new();
        for r in rs {
            let Some(p) = r.published_at else { continue };
            if r.builtin || r.status == STATUS_DRAFT || p > at {
                continue;
            }
            if when.get(&r.domain.name).is_none_or(|t| p > *t) {
                when.insert(r.domain.name.clone(), p);
                out.insert(r.domain.name.clone(), r.domain);
            }
        }
        Ok(out)
    }

    /// pinnedDomain is a version of a domain a methodology pins: published or archived.
    async fn pinned_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Domain> {
        if version.is_empty() {
            return Err(Error::Other(format!(
                "domain {name} is pinned with no version"
            )));
        }
        let r = self
            .get_domain(ctx, name, version)
            .await
            .map_err(|e| Error::Other(format!("pinned domain {name}@{version}: {e}")))?;
        if r.status == STATUS_DRAFT {
            return Err(Error::Other(format!(
                "pinned domain {name}@{version} is a draft: a methodology is written against published domains"
            )));
        }
        Ok(r.domain)
    }

    /// typesFor is the type catalogue of a methodology version: the built-in domains and domainsFor.
    pub(crate) async fn types_for(
        &self,
        ctx: &Ctx,
        m: &Methodology,
    ) -> Result<(Catalog, Vec<Domain>)> {
        let ds = self.domains_for(ctx, m, None).await?;
        let cat = goap_typecat::new(&ds)?;
        Ok((cat, ds))
    }

    /// domainPinIssues are the issues of the pins of a methodology: a built-in domain is never pinned, the others
    /// name a published (or archived) version.
    pub(crate) async fn domain_pin_issues(&self, ctx: &Ctx, m: &Methodology) -> Issues {
        let mut issues = Issues::default();
        for (name, v) in &m.domains {
            let path = format!("domains.{name}");
            if goap_typecat::is_builtin(name) {
                issues.0.push(Issue::new(
                    path,
                    format!(
                        "domain {name} is built into the platform: it changes with the platform, it is not pinned"
                    ),
                ));
                continue;
            }
            if let Err(e) = self.pinned_domain(ctx, name, v).await {
                issues.0.push(Issue::new(path, e.to_string()));
            }
        }
        issues
    }

    /// pinDomains pins, on a methodology being published, every published domain in force it does not pin yet: the
    /// published version keeps the types it was validated against (ADR 0113).
    pub(crate) async fn pin_domains(&self, ctx: &Ctx, m: &mut Methodology) -> Result<()> {
        for d in self.domains_in_force(ctx).await? {
            m.domains.entry(d.name.clone()).or_insert(d.version.clone());
        }
        Ok(())
    }
}
