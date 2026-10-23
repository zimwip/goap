//! What the graph lists of itself for its browsers (Go's `graph/graph/browse.go`).
use graph_core::GraphError;

use super::Session;

impl Session {
    /// The namespaces some node lives in, sorted (`Tx.Namespaces`).
    pub async fn namespaces(&self) -> Result<Vec<String>, GraphError> {
        let rows = self
            .select(
                (
                    "SELECT DISTINCT namespace FROM node ORDER BY namespace".into(),
                    Vec::new(),
                ),
                "namespaces",
            )
            .await?;
        Ok(rows.iter().map(|r| r.text(0)).collect())
    }
}

impl Session {
    /// Whether a node of the key has a version in the namespace (`Tx.NodeByKey` finding one).
    pub async fn has_node(&self, namespace: &str, key: &str) -> Result<bool, GraphError> {
        let q = format!(
            "SELECT 1 FROM node n WHERE n.namespace = {} AND n.key = {} \
             AND EXISTS (SELECT 1 FROM node_version v WHERE v.node_id = n.id)",
            self.sql.ph(1),
            self.sql.ph(2)
        );
        Ok(!self
            .select((q, vec![namespace.into(), key.into()]), "node")
            .await?
            .is_empty())
    }
}
