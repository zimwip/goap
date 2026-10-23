//! The domain versions the registry stores (`graph/domainsvc.SQLStore`, ADR 0125 §3), read to build the type catalogue.
use graph_catalog::{
    def::Domain,
    record::{Record, parse_time},
};
use graph_core::{GraphError, store::OutboxMessage};

use super::{Session, value::Arg};

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
