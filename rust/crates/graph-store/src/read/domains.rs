//! The domain versions the registry stores (`graph/domainsvc.SQLStore`, ADR 0125 §3), read to build the type catalogue.
use graph_catalog::{
    def::Domain,
    record::{Record, parse_time},
};
use graph_core::{GraphError, store::OutboxMessage};

use graph_catalog::record::{STATUS_DRAFT, STATUS_PUBLISHED};

use super::{
    Session,
    value::{Arg, Val},
};

const COLUMNS: &str = "name, version, status, definition, created_at, updated_at, published_at, updated_by";

impl Session {
    /// Every stored domain version, in the order of the table (`ListDomains`).
    pub async fn domain_records(&self) -> Result<Vec<Record>, GraphError> {
        let rows = self
            .rows((format!("SELECT {COLUMNS} FROM domain_version"), Vec::new()))
            .await?;
        rows.iter()
            .map(|r| {
                let (name, version) = (r.text(0), r.text(1));
                let mut domain = Domain::from_json(&r.text(3))
                    .map_err(|e| GraphError::internal(format!("domain {name}@{version}: {e}")))?;
                domain.name.clone_from(&name);
                domain.version.clone_from(&version);
                let time = |i: usize| parse_time(&r.text(i)).map_err(GraphError::internal);
                Ok(Record {
                    domain,
                    status: r.text(2),
                    created_at: time(4)?,
                    updated_at: time(5)?,
                    published_at: time(6)?,
                    updated_by: r.text(7),
                })
            })
            .collect()
    }

    /// What tells the stored domain versions apart without reading their definitions: a version saved, published or
    /// archived changes it.
    pub async fn domain_fingerprint(&self) -> Result<String, GraphError> {
        let rows = self
            .rows((
                "SELECT name, version, status, updated_at, published_at FROM domain_version ORDER BY name, version"
                    .into(),
                Vec::new(),
            ))
            .await?;
        Ok(rows
            .iter()
            .map(|r| (0..5).map(|i| r.text(i)).collect::<Vec<_>>().join("\u{1f}"))
            .collect::<Vec<_>>()
            .join("\u{1e}"))
    }

    /// The status of a stored version, none when it is not stored.
    pub async fn domain_status(&self, name: &str, version: &str) -> Result<Option<String>, GraphError> {
        let q = format!(
            "SELECT status FROM domain_version WHERE name = {} AND version = {}",
            self.sql.ph(1),
            self.sql.ph(2)
        );
        let rows = self.select((q, vec![name.into(), version.into()]), "domain").await?;
        Ok(rows.first().map(|r| r.text(0)))
    }

    /// Stores a new domain version with the events of its write (`SQLStore.SaveDomain` of a version not stored yet),
    /// in the transaction of the session.
    pub async fn insert_domain(&self, r: &NewDomain<'_>, events: &[OutboxMessage]) -> Result<(), GraphError> {
        let q = format!(
            "INSERT INTO domain_version ({COLUMNS}) VALUES ({})",
            (1..=8).map(|i| self.sql.ph(i)).collect::<Vec<_>>().join(", ")
        );
        let args: Vec<Arg> = vec![
            r.name.into(),
            r.version.into(),
            r.status.into(),
            r.definition.into(),
            r.created_at.into(),
            r.updated_at.into(),
            r.published_at.into(),
            r.updated_by.into(),
        ];
        self.run((q, args), "domain").await?;
        self.append_to_outbox("domain_outbox", events).await
    }

    /// Moves a version to a status (`SQLStore.SetDomainStatus`), stamping its publication when it is published; false
    /// when there is no such version.
    pub async fn set_domain_status(
        &self,
        name: &str,
        version: &str,
        status: &str,
        at: &str,
        events: &[OutboxMessage],
    ) -> Result<bool, GraphError> {
        let ph = |i| self.sql.ph(i);
        let (q, mut args): (String, Vec<Arg>) = if status == STATUS_PUBLISHED {
            (
                format!(
                    "UPDATE domain_version SET status = {}, updated_at = {}, published_at = {} WHERE name = {} AND version = {}",
                    ph(1),
                    ph(2),
                    ph(3),
                    ph(4),
                    ph(5)
                ),
                vec![status.into(), at.into(), at.into()],
            )
        } else {
            (
                format!(
                    "UPDATE domain_version SET status = {}, updated_at = {} WHERE name = {} AND version = {}",
                    ph(1),
                    ph(2),
                    ph(3),
                    ph(4)
                ),
                vec![status.into(), at.into()],
            )
        };
        args.extend([name.into(), version.into()]);
        if self.run((q, args), "domain").await? == 0 {
            return Ok(false);
        }
        self.append_to_outbox("domain_outbox", events).await?;
        Ok(true)
    }

    /// Removes a draft (`SQLStore.DeleteDomain`).
    pub async fn delete_domain_draft(
        &self,
        name: &str,
        version: &str,
        events: &[OutboxMessage],
    ) -> Result<(), GraphError> {
        let q = format!(
            "DELETE FROM domain_version WHERE name = {} AND version = {} AND status = {}",
            self.sql.ph(1),
            self.sql.ph(2),
            self.sql.ph(3)
        );
        self.run((q, vec![name.into(), version.into(), STATUS_DRAFT.into()]), "domain")
            .await?;
        self.append_to_outbox("domain_outbox", events).await
    }
}

/// A stored version as read, its definition the JSON as stored.
pub struct StoredDomain {
    pub name: String,
    pub version: String,
    pub status: String,
    pub definition: serde_json::Value,
    /// The definition as the database holds it: Go's JSON on SQLite, the text of the `jsonb` on PostgreSQL.
    pub definition_text: String,
    pub created_at: Option<chrono::DateTime<chrono::Utc>>,
    pub updated_at: Option<chrono::DateTime<chrono::Utc>>,
    pub published_at: Option<chrono::DateTime<chrono::Utc>>,
    pub updated_by: String,
}

impl Session {
    /// A stored version (`SQLStore.GetDomain`): the one named, or with no version the latest published.
    pub async fn stored_domain(&self, name: &str, version: &str) -> Result<Option<StoredDomain>, GraphError> {
        let (filter, second) = if version.is_empty() {
            ("status", STATUS_PUBLISHED)
        } else {
            ("version", version)
        };
        let q = format!(
            "SELECT {COLUMNS} FROM domain_version WHERE name = {} AND {filter} = {}",
            self.sql.ph(1),
            self.sql.ph(2)
        );
        let rows = self.select((q, vec![name.into(), second.into()]), "domain").await?;
        let mut best: Option<StoredDomain> = None;
        for r in &rows {
            let time = |i: usize| parse_time(&r.text(i)).map_err(GraphError::internal);
            let (definition, definition_text) = match r.0.get(3) {
                Some(Val::Json(j)) => (j.clone(), j.to_string()),
                _ => (
                    serde_json::from_str(&r.text(3)).map_err(GraphError::internal)?,
                    r.text(3),
                ),
            };
            let d = StoredDomain {
                name: r.text(0),
                version: r.text(1),
                status: r.text(2),
                definition,
                definition_text,
                created_at: time(4)?,
                updated_at: time(5)?,
                published_at: time(6)?,
                updated_by: r.text(7),
            };
            if best.as_ref().is_none_or(|b| d.published_at > b.published_at) {
                best = Some(d);
            }
        }
        Ok(best)
    }
}

/// A domain version to store: its definition is the JSON of the domain, its times RFC 3339 texts (empty: none).
pub struct NewDomain<'a> {
    pub name: &'a str,
    pub version: &'a str,
    pub status: &'a str,
    pub definition: &'a str,
    pub created_at: &'a str,
    pub updated_at: &'a str,
    pub published_at: &'a str,
    pub updated_by: &'a str,
}
