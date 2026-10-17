//! A distributed lock over a NATS JetStream KV bucket.
use std::time::Duration;

use async_nats::jetstream::kv::{self, CreateErrorKind, Operation};
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use serde::{Deserialize, Serialize};

use crate::{Events, PlatformError, Result};

/// Lease is a distributed lock over a NATS JetStream KV bucket, used to elect a single leader among several replicas
/// of the same service (e.g. which engine replica may fire schedule/cron triggers).
#[derive(Clone)]
pub struct Lease {
    kv: kv::Store,
}

#[derive(Serialize, Deserialize)]
struct LeaseValue {
    holder: String,
    #[serde(rename = "renewedAt")]
    renewed_at: DateTime<Utc>,
}

impl Events {
    /// Ensures the JetStream KV bucket exists and returns a [`Lease`] over it, reusing this connection.
    pub async fn new_lease(&self, _ctx: &Ctx, bucket: &str) -> Result<Lease> {
        let kv = self
            .js
            .create_or_update_key_value(kv::Config {
                bucket: bucket.to_string(),
                ..Default::default()
            })
            .await
            .map_err(PlatformError::nats)?;
        Ok(Lease { kv })
    }
}

impl Lease {
    /// Takes or renews the lease identified by `key` for `holder`. True when `holder` now holds the lease (fresh
    /// acquisition, or renewal of a lease it already held), false when another holder's lease is still within
    /// `ttl`. The caller is expected to call it again well before `ttl` elapses (e.g. every ttl/3) to keep renewing.
    pub async fn acquire(
        &self,
        _ctx: &Ctx,
        key: &str,
        holder: &str,
        ttl: Duration,
    ) -> Result<bool> {
        let entry = self.kv.entry(key).await.map_err(PlatformError::nats)?;
        let fresh = serde_json::to_vec(&LeaseValue {
            holder: holder.to_string(),
            renewed_at: Utc::now(),
        })?;
        let entry = match entry {
            Some(e) if e.operation == Operation::Put => e,
            _ => {
                return match self.kv.create(key, fresh.into()).await {
                    Ok(_) => Ok(true),
                    Err(e) if e.kind() == CreateErrorKind::AlreadyExists => Ok(false), // someone else just took it
                    Err(e) => Err(PlatformError::nats(e)),
                };
            }
        };
        let cur: LeaseValue = serde_json::from_slice(&entry.value)?;
        let age = (Utc::now() - cur.renewed_at)
            .to_std()
            .unwrap_or(Duration::ZERO);
        if cur.holder != holder && age < ttl {
            return Ok(false); // still held by someone else
        }
        // losing the race to another replica's renewal or steal is no error
        Ok(self
            .kv
            .update(key, fresh.into(), entry.revision)
            .await
            .is_ok())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A live Events connection, or None when GOAP_TEST_NATS_URL is not set (mirrors GOAP_TEST_PG_DSN).
    async fn test_events() -> Option<Events> {
        let url = std::env::var("GOAP_TEST_NATS_URL").ok()?;
        Some(Events::connect(&Ctx::background(), &url).await.unwrap())
    }

    #[tokio::test]
    async fn lease_acquire_and_steal() {
        let Some(e) = test_events().await else { return };
        let ctx = Ctx::background();
        let lease = e.new_lease(&ctx, "test-leases").await.unwrap();
        let key = "trigger-manager";
        let ttl = Duration::from_millis(200);

        assert!(
            lease.acquire(&ctx, key, "a", ttl).await.unwrap(),
            "a should acquire the free lease"
        );
        assert!(
            !lease.acquire(&ctx, key, "b", ttl).await.unwrap(),
            "b must not acquire a's fresh lease"
        );
        assert!(
            lease.acquire(&ctx, key, "a", ttl).await.unwrap(),
            "a should renew its own lease"
        );

        tokio::time::sleep(2 * ttl).await;
        assert!(
            lease.acquire(&ctx, key, "b", ttl).await.unwrap(),
            "b should steal a's expired lease"
        );
        assert!(
            !lease.acquire(&ctx, key, "a", ttl).await.unwrap(),
            "a must not reacquire after b stole the lease"
        );
        e.close().await;
    }
}
