//! The relay's side of an outbox table (`lib/outbox.SQL`, ADR 0136): the messages not published yet, oldest first,
//! marked once delivered and purged after their retention.
use std::collections::BTreeMap;

use chrono::{DateTime, Utc};
use graph_core::GraphError;

use super::{Session, value::Arg};

/// A message of an outbox as its relay reads it.
#[derive(Debug, Clone)]
pub struct Pending {
    pub seq: i64,
    pub subject: String,
    /// The payload as the database gives it back as text (PostgreSQL's form of a jsonb), as Go's relay reads it.
    pub data: String,
    pub headers: BTreeMap<String, String>,
    pub created_at: Option<DateTime<Utc>>,
}

impl Session {
    /// At most `limit` messages of `table` not published yet, oldest first (`SQL.Pending`).
    pub async fn outbox_pending(&self, table: &str, limit: i64) -> Result<Vec<Pending>, GraphError> {
        let q = format!(
            "SELECT seq, subject, CAST(payload AS TEXT), CAST(headers AS TEXT), created_at FROM {table}
		WHERE published_at IS NULL ORDER BY seq LIMIT {}",
            self.sql.ph(1)
        );
        let rows = self.select((q, vec![Arg::Int(limit)]), "outbox").await?;
        Ok(rows
            .iter()
            .map(|r| Pending {
                seq: r.int(0),
                subject: r.text(1),
                data: r.text(2),
                // Go ignores headers it cannot read
                headers: serde_json::from_str(&r.text(3)).unwrap_or_default(),
                created_at: r.time(4),
            })
            .collect())
    }

    /// Records that the messages `seqs` of `table` were delivered at `at` (`SQL.MarkPublished`).
    pub async fn outbox_mark(&self, table: &str, seqs: &[i64], at: DateTime<Utc>) -> Result<(), GraphError> {
        if seqs.is_empty() {
            return Ok(());
        }
        let marks: Vec<String> = (0..seqs.len()).map(|i| self.sql.ph(i + 2)).collect();
        let q = format!(
            "UPDATE {table} SET published_at = {} WHERE seq IN ({})",
            self.sql.ph(1),
            marks.join(", ")
        );
        let mut args = vec![Arg::Time(at)];
        args.extend(seqs.iter().map(|s| Arg::Int(*s)));
        self.run((q, args), "outbox").await.map(|_| ())
    }

    /// Removes the messages of `table` published before `before` (`SQL.PurgePublished`).
    pub async fn outbox_purge(&self, table: &str, before: DateTime<Utc>) -> Result<(), GraphError> {
        let q = format!(
            "DELETE FROM {table} WHERE published_at IS NOT NULL AND published_at < {}",
            self.sql.ph(1)
        );
        self.run((q, vec![Arg::Time(before)]), "outbox").await.map(|_| ())
    }
}
