//! The registry of connectors and its in-memory store (`store.go`).

use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_gen::connector::v1::ConnectorInfo;
use parking_lot::Mutex;

use crate::Error;

/// ConnectorReg is the registration of a connector: what it announced and where it is.
#[derive(Debug, Clone, PartialEq)]
pub struct ConnectorReg {
    pub info: ConnectorInfo,
    pub endpoint: String,
    pub last_seen: DateTime<Utc>,
}

/// Store persists the registry of connectors.
#[async_trait]
pub trait Store: Send + Sync {
    async fn save_connector(&self, ctx: &Ctx, r: ConnectorReg) -> Result<(), Error>;
    async fn connector(&self, ctx: &Ctx, id: &str) -> Result<ConnectorReg, Error>;
    /// Connectors lists the registrations by id.
    async fn connectors(&self, ctx: &Ctx) -> Result<Vec<ConnectorReg>, Error>;
}

/// MemoryStore is an in-memory Store.
#[derive(Debug, Default)]
pub struct MemoryStore {
    connectors: Mutex<HashMap<String, ConnectorReg>>,
}

impl MemoryStore {
    /// NewMemoryStore returns an empty in-memory store.
    pub fn new() -> Self {
        Self::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn save_connector(&self, _ctx: &Ctx, r: ConnectorReg) -> Result<(), Error> {
        self.connectors.lock().insert(r.info.id.clone(), r);
        Ok(())
    }

    async fn connector(&self, _ctx: &Ctx, id: &str) -> Result<ConnectorReg, Error> {
        self.connectors
            .lock()
            .get(id)
            .cloned()
            .ok_or_else(|| Error::not_found(format!("connector {id}")))
    }

    async fn connectors(&self, _ctx: &Ctx) -> Result<Vec<ConnectorReg>, Error> {
        let mut out: Vec<ConnectorReg> = self.connectors.lock().values().cloned().collect();
        out.sort_by(|a, b| a.info.id.cmp(&b.info.id));
        Ok(out)
    }
}
