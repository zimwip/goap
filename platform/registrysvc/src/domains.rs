//! The port of the methodology registry on the domain registry (ADR 0125 §3).

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_def::{Domain, Record};

use crate::error::{Error, Result};
use crate::service::Service;
use crate::store::{Record as MethodologyRecord, STATUS_ARCHIVED, STATUS_PUBLISHED};

/// Domains is the port of the methodology registry on the domain registry (ADR 0125 §3): the methodologies are written
/// against the domains, the domains know no methodology. `goap_domainclient::Client` implements it over RPC;
/// in process the compositions wrap `goap_domainsvc::Service` in a newtype implementing it (this crate may not depend
/// on the domain registry, ADR 0125 §3).
#[async_trait]
pub trait Domains: Send + Sync {
    /// DomainVersions returns every version, or the latest version of each domain (latest published if any, latest
    /// draft otherwise); the built-in domains come first.
    async fn domain_versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<Record>>;
    /// GetDomain returns a version (empty version: the latest published one), the built-in domains included.
    async fn get_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record>;
}

#[async_trait]
impl Domains for goap_domainclient::Client {
    async fn domain_versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<Record>> {
        Ok(goap_domainclient::Client::domain_versions(self, ctx, all).await?)
    }
    async fn get_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record> {
        Ok(goap_domainclient::Client::get_domain(self, ctx, name, version).await?)
    }
}

/// builtinRecords are the records of the domains shipped with the platform: published, frozen, never stored.
pub(crate) fn builtin_records() -> Vec<Record> {
    goap_typecat::builtins()
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

impl Service {
    /// domainVersions reads the domain registry; with none, the built-in domains only.
    pub(crate) async fn domain_versions(&self, ctx: &Ctx, all: bool) -> Result<Vec<Record>> {
        match &self.domains {
            None => Ok(builtin_records()),
            Some(d) => d.domain_versions(ctx, all).await,
        }
    }

    /// getDomain reads a version from the domain registry; with none, a built-in domain only.
    pub(crate) async fn get_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record> {
        if let Some(d) = &self.domains {
            return d.get_domain(ctx, name, version).await;
        }
        builtin_records()
            .into_iter()
            .find(|r| r.domain.name == name && (version.is_empty() || version == r.domain.version))
            .ok_or_else(|| Error::not_found(format!("domain {name}@{version}")))
    }

    /// domainsInForce are the latest published version of every stored domain (the built-in ones left out: a type
    /// catalogue adds them itself).
    pub(crate) async fn domains_in_force(&self, ctx: &Ctx) -> Result<Vec<Domain>> {
        let rs = self.domain_versions(ctx, false).await?;
        Ok(rs
            .into_iter()
            .filter(|r| r.status == STATUS_PUBLISHED && !r.builtin)
            .map(|r| r.domain)
            .collect())
    }

    /// DomainUsage lists the methodology versions (not archived) that act on the namespace of a domain or reference
    /// its types; each follows the version it pins (Methodology.domains, ADR 0113), a draft the version in force.
    pub async fn domain_usage(
        &self,
        ctx: &Ctx,
        name: &str,
        _version: &str,
    ) -> Result<Vec<MethodologyRecord>> {
        let rs = self.store.list(ctx).await?;
        Ok(rs
            .into_iter()
            .filter(|r| {
                r.status != STATUS_ARCHIVED && r.methodology.namespaces().iter().any(|n| n == name)
            })
            .collect())
    }
}
