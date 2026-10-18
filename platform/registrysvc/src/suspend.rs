//! Suspension (ADR 0125 §3): the domain registry publishes a domain without asking the
//! methodologies: it emits an event, and the methodology registry checks its methodologies again. A published version
//! that no longer compiles against the domains in force (the latest published version of each, what the graph judges
//! its writes by) is suspended, never the domain nor the graph: no change starts on it (MethodologyVersion), no run
//! reads it (Methodology, List), until a compatible version is published or the domains come back. A version still
//! compiles against the versions it pins (ADR 0113) while it is in force; the pins do not shield it from the graph it
//! writes to. Nothing is stored: a suspension is what the compiler says of the version against the domains of now.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_engine::Payload;

use crate::error::Result;
use crate::service::Service;
use crate::store::{Record, STATUS_PUBLISHED, key};

impl Service {
    /// Suspended says why a stored version is suspended; "" when it is in force, a draft or archived, or when the
    /// domains cannot be read (not a reason to suspend).
    pub async fn suspended(&self, ctx: &Ctx, r: &Record) -> String {
        match self.suspension(ctx, r).await {
            Ok(Some(why)) => why,
            _ => String::new(),
        }
    }

    /// suspension is the refusal of the compiler for a published version against the domains in force (None: in
    /// force); Err a failure to read the domains.
    async fn suspension(&self, ctx: &Ctx, r: &Record) -> Result<Option<String>> {
        if r.status != STATUS_PUBLISHED {
            return Ok(None);
        }
        let ds = self.domains_in_force(ctx).await?;
        Ok(self.compile_against(r, &ds).err().map(|e| e.to_string()))
    }

    /// DomainsChanged is the methodology registry's answer to an event of the domain registry (a version published or
    /// archived): the compiled methodologies are dropped and every published version is checked again (Recheck).
    pub async fn domains_changed(&self, ctx: &Ctx) -> Result<BTreeMap<String, String>> {
        self.compiled.clear();
        self.recheck(ctx).await
    }

    /// Recheck checks every published methodology version against the domains in force, and returns the suspended
    /// ones (name@version → reason). A version that becomes suspended, or comes back in force, since the last check is
    /// logged and published (goap.registry.methodology.suspended | resumed, payload {name, version, status, reason}).
    pub async fn recheck(&self, ctx: &Ctx) -> Result<BTreeMap<String, String>> {
        let rs = self.store.list(ctx).await?;
        let mut now: BTreeMap<String, String> = BTreeMap::new();
        let mut by_key: BTreeMap<String, Record> = BTreeMap::new();
        for r in rs {
            if r.status != STATUS_PUBLISHED {
                continue;
            }
            let k = key(&r.methodology.name, &r.methodology.version);
            if let Some(why) = self.suspension(ctx, &r).await? {
                now.insert(k.clone(), why);
            }
            by_key.insert(k, r);
        }
        let before = std::mem::replace(&mut *self.suspended.lock(), now.clone());
        for (k, why) in &now {
            if !before.contains_key(k) {
                tracing::warn!(methodology = %k, reason = %why,
                    "methodology suspended: it no longer compiles against the domains in force");
                if let Some(r) = by_key.get(k) {
                    self.publish_suspension(ctx, "suspended", r, why).await;
                }
            }
        }
        for k in before.keys() {
            if !now.contains_key(k) {
                tracing::info!(methodology = %k, "methodology back in force");
                if let Some(r) = by_key.get(k) {
                    self.publish_suspension(ctx, "resumed", r, "").await;
                }
            }
        }
        Ok(now)
    }

    async fn publish_suspension(&self, ctx: &Ctx, event: &str, r: &Record, reason: &str) {
        if let Some(ev) = &self.events {
            let v: BTreeMap<String, String> = BTreeMap::from([
                ("name".to_string(), r.methodology.name.clone()),
                ("version".to_string(), r.methodology.version.clone()),
                ("status".to_string(), r.status.to_string()),
                ("reason".to_string(), reason.to_string()),
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
}
