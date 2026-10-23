//! The domain versions the registry stores (`graph/domainsvc.SQLStore`, ADR 0125 §3), read to build the type catalogue.
use graph_catalog::{
    def::Domain,
    record::{Record, parse_time},
};
use graph_core::GraphError;

use super::Session;

const COLUMNS: &str = "name, version, status, definition, created_at, updated_at, published_at, updated_by";

impl Session {
    /// Every stored domain version, in the order of the table (`ListDomains`).
    pub async fn domain_records(&self) -> Result<Vec<Record>, GraphError> {
        let rows = self.rows((format!("SELECT {COLUMNS} FROM domain_version"), Vec::new())).await?;
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
                "SELECT name, version, status, updated_at, published_at FROM domain_version ORDER BY name, version".into(),
                Vec::new(),
            ))
            .await?;
        Ok(rows
            .iter()
            .map(|r| (0..5).map(|i| r.text(i)).collect::<Vec<_>>().join("\u{1f}"))
            .collect::<Vec<_>>()
            .join("\u{1e}"))
    }
}
