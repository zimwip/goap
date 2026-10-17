//! Port of `store.go`: the stored methodology versions and the [`Store`] that persists them.

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_methodology::Methodology;

use crate::error::{Error, Result};
use crate::graphstore::StoreGraph;

/// Status of a methodology version: the lifecycle of a domain version (def.Status).
pub type Status = goap_def::Status;

pub const STATUS_DRAFT: Status = goap_def::STATUS_DRAFT;
pub const STATUS_PUBLISHED: Status = goap_def::STATUS_PUBLISHED;
pub const STATUS_ARCHIVED: Status = goap_def::STATUS_ARCHIVED;

/// Record is a stored methodology version. The times are None where Go's are zero.
#[derive(Debug, Clone, Default)]
pub struct Record {
    pub methodology: Methodology,
    pub status: Status,
    pub created_at: Option<DateTime<Utc>>,
    pub updated_at: Option<DateTime<Utc>>,
    pub published_at: Option<DateTime<Utc>>,
    pub updated_by: String,
}

/// Store persists methodology versions.
#[async_trait]
pub trait Store: Send + Sync {
    /// Save creates or replaces a draft. It fails with ErrImmutable when the version exists and is not a draft.
    async fn save(&self, ctx: &Ctx, r: Record) -> Result<()>;
    /// Get returns a version; an empty version returns the latest published one.
    async fn get(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record>;
    /// List returns every version, sorted by name then creation date.
    async fn list(&self, ctx: &Ctx) -> Result<Vec<Record>>;
    async fn set_status(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
        s: Status,
        at: DateTime<Utc>,
    ) -> Result<()>;
    /// Delete removes a draft.
    async fn delete(&self, ctx: &Ctx, name: &str, version: &str) -> Result<()>;
    /// The graph behind the store, if any (Go's `graphBacked` assertion): where the registry proposes the alias stubs
    /// and reads the sub-activities. None: no graph (a MemoryStore).
    fn graph(&self) -> Option<Arc<dyn StoreGraph>> {
        None
    }
}

/// The key of a version: `name@version`.
pub fn key(name: &str, version: &str) -> String {
    format!("{name}@{version}")
}

/// `t.After(u)` with None as Go's zero time.
pub(crate) fn after(t: &Option<DateTime<Utc>>, u: &Option<DateTime<Utc>>) -> bool {
    match (t, u) {
        (Some(t), Some(u)) => t > u,
        (Some(_), None) => true,
        _ => false,
    }
}

/// The records sorted by name then creation date (stable).
pub(crate) fn sort_records(rs: &mut [Record]) {
    rs.sort_by(|a, b| {
        a.methodology
            .name
            .cmp(&b.methodology.name)
            .then_with(|| a.created_at.cmp(&b.created_at))
    });
}

/// MemoryStore is an in-memory Store.
#[derive(Default)]
pub struct MemoryStore {
    m: parking_lot::RwLock<HashMap<String, Record>>,
}

impl MemoryStore {
    /// NewMemoryStore returns an empty store.
    pub fn new() -> MemoryStore {
        MemoryStore::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn save(&self, _: &Ctx, mut r: Record) -> Result<()> {
        let mut m = self.m.write();
        let k = key(&r.methodology.name, &r.methodology.version);
        if let Some(old) = m.get(&k) {
            if old.status != STATUS_DRAFT {
                return Err(Error::immutable(&k));
            }
            r.created_at = old.created_at;
        }
        m.insert(k, r);
        Ok(())
    }

    async fn get(&self, _: &Ctx, name: &str, version: &str) -> Result<Record> {
        let m = self.m.read();
        if !version.is_empty() {
            return m
                .get(&key(name, version))
                .cloned()
                .ok_or_else(|| Error::not_found(key(name, version)));
        }
        let mut best: Option<&Record> = None;
        for r in m.values() {
            if r.methodology.name == name
                && r.status == STATUS_PUBLISHED
                && best.is_none_or(|b| after(&r.published_at, &b.published_at))
            {
                best = Some(r);
            }
        }
        best.cloned()
            .ok_or_else(|| Error::not_found(format!("{name} (published)")))
    }

    async fn list(&self, _: &Ctx) -> Result<Vec<Record>> {
        let mut out: Vec<Record> = self.m.read().values().cloned().collect();
        // a map has no order: by key first, so that equal creation dates keep a stable order
        out.sort_by(|a, b| {
            key(&a.methodology.name, &a.methodology.version)
                .cmp(&key(&b.methodology.name, &b.methodology.version))
        });
        sort_records(&mut out);
        Ok(out)
    }

    async fn set_status(
        &self,
        _: &Ctx,
        name: &str,
        version: &str,
        st: Status,
        at: DateTime<Utc>,
    ) -> Result<()> {
        let mut m = self.m.write();
        let k = key(name, version);
        let Some(r) = m.get_mut(&k) else {
            return Err(Error::not_found(&k));
        };
        r.status = st.clone();
        r.updated_at = Some(at);
        if st == STATUS_PUBLISHED {
            r.published_at = Some(at);
        }
        Ok(())
    }

    async fn delete(&self, _: &Ctx, name: &str, version: &str) -> Result<()> {
        let mut m = self.m.write();
        let k = key(name, version);
        let Some(r) = m.get(&k) else {
            return Err(Error::not_found(&k));
        };
        if r.status != STATUS_DRAFT {
            return Err(Error::immutable(&k));
        }
        m.remove(&k);
        Ok(())
    }
}
