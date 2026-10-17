//! The store of the domain versions and its in-memory implementation.

use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_def::{Record, STATUS_DRAFT, STATUS_PUBLISHED, Status};
use parking_lot::RwLock;

use crate::error::Error;

/// Store persists domain versions (same lifecycle as methodologies): the database of the domain registry
/// ([`crate::SqlStore`]), or the [`MemoryStore`].
#[async_trait]
pub trait Store: Send + Sync {
    /// SaveDomain creates or replaces a draft (`Error::Immutable` otherwise).
    async fn save_domain(&self, ctx: &Ctx, r: Record) -> Result<(), Error>;
    /// GetDomain returns a version; an empty version returns the latest published one.
    async fn get_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record, Error>;
    /// ListDomains returns every version, sorted by name then creation date.
    async fn list_domains(&self, ctx: &Ctx) -> Result<Vec<Record>, Error>;
    async fn set_domain_status(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
        s: Status,
        at: DateTime<Utc>,
    ) -> Result<(), Error>;
    /// DeleteDomain removes a draft.
    async fn delete_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<(), Error>;
}

/// Sorts records by name then creation date (stable).
pub(crate) fn sort_domain_records(rs: &mut [Record]) {
    rs.sort_by(|a, b| {
        a.domain
            .name
            .cmp(&b.domain.name)
            .then_with(|| a.created_at.cmp(&b.created_at))
    });
}

pub(crate) fn key(name: &str, version: &str) -> String {
    format!("{name}@{version}")
}

/// MemoryStore is an in-memory Store.
#[derive(Default)]
pub struct MemoryStore {
    m: RwLock<HashMap<String, Record>>,
}

impl MemoryStore {
    /// NewMemoryStore returns an empty store.
    pub fn new() -> Self {
        Self::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn save_domain(&self, _ctx: &Ctx, mut r: Record) -> Result<(), Error> {
        let mut m = self.m.write();
        let k = key(&r.domain.name, &r.domain.version);
        if let Some(old) = m.get(&k) {
            if old.status != STATUS_DRAFT {
                return Err(Error::immutable(format!("domain {k}")));
            }
            r.created_at = old.created_at;
        }
        m.insert(k, r);
        Ok(())
    }

    async fn get_domain(&self, _ctx: &Ctx, name: &str, version: &str) -> Result<Record, Error> {
        let m = self.m.read();
        if !version.is_empty() {
            return m
                .get(&key(name, version))
                .cloned()
                .ok_or_else(|| Error::not_found(key(name, version)));
        }
        let mut best: Option<&Record> = None;
        for r in m.values() {
            if r.domain.name == name
                && r.status == STATUS_PUBLISHED
                && best.is_none_or(|b| r.published_at > b.published_at)
            {
                best = Some(r);
            }
        }
        best.cloned()
            .ok_or_else(|| Error::not_found(format!("{name} (published)")))
    }

    async fn list_domains(&self, _ctx: &Ctx) -> Result<Vec<Record>, Error> {
        let mut out: Vec<Record> = self.m.read().values().cloned().collect();
        sort_domain_records(&mut out);
        Ok(out)
    }

    async fn set_domain_status(
        &self,
        _ctx: &Ctx,
        name: &str,
        version: &str,
        st: Status,
        at: DateTime<Utc>,
    ) -> Result<(), Error> {
        let mut m = self.m.write();
        let k = key(name, version);
        let Some(r) = m.get_mut(&k) else {
            return Err(Error::not_found(k));
        };
        if st == STATUS_PUBLISHED {
            r.published_at = Some(at);
        }
        r.status = st;
        r.updated_at = Some(at);
        Ok(())
    }

    async fn delete_domain(&self, _ctx: &Ctx, name: &str, version: &str) -> Result<(), Error> {
        let mut m = self.m.write();
        let k = key(name, version);
        let Some(r) = m.get(&k) else {
            return Err(Error::not_found(k));
        };
        if r.status != STATUS_DRAFT {
            return Err(Error::immutable(format!("domain {k}")));
        }
        m.remove(&k);
        Ok(())
    }
}
