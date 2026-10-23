//! What the graph lists of itself for its browsers (Go's `graph/graph/browse.go`).
use chrono::{DateTime, Utc};
use graph_core::GraphError;

use super::{Session, value::Arg};

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

/// A tag: a name on one node version (ADR 0143 §6).
#[derive(Debug, Clone, PartialEq)]
pub struct Tag {
    pub id: String,
    pub name: String,
    pub namespace: String,
    pub node: String,
    pub version: i32,
    pub by: String,
    pub created_at: Option<DateTime<Utc>>,
}

impl Session {
    /// The tags of a namespace, of a name and of a node, each when given, oldest first (`Tx.Tags`).
    pub async fn tags(&self, namespace: &str, name: &str, node: &str) -> Result<Vec<Tag>, GraphError> {
        let s = self.sql;
        if s.pg && !node.is_empty() && uuid::Uuid::try_parse(node).is_err() {
            return Err(GraphError::unimplemented(
                "a node id PostgreSQL does not read: the error Go words is not served here yet",
            ));
        }
        let mut q = format!(
            "SELECT {}, name, namespace, {}, version, by, created_at FROM tag WHERE {}",
            s.id("id"),
            s.id("node_id"),
            if s.pg { "true" } else { "1 = 1" }
        );
        let mut args: Vec<Arg> = Vec::new();
        for (col, v) in [("namespace", namespace), ("name", name), ("node_id", node)] {
            if v.is_empty() {
                continue;
            }
            args.push(v.into());
            q.push_str(&format!(" AND {col} = {}", s.ph(args.len())));
        }
        q.push_str(&if s.pg {
            " ORDER BY created_at, id".to_string()
        } else {
            s.order_by("created_at")
        });
        Ok(self
            .select((q, args), "tags")
            .await?
            .iter()
            .map(|r| Tag {
                id: r.text(0),
                name: r.text(1),
                namespace: r.text(2),
                node: r.text(3),
                version: r.int32(4),
                by: r.text(5),
                created_at: r.time(6),
            })
            .collect())
    }
}
