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

impl Session {
    /// A page of the links between two of the versions refs (`Tx.LinkPage`): the links, how many the type keeps, the
    /// counts of every type.
    pub async fn link_page(
        &self,
        refs: &[graph_core::model::NodeRef],
        typ: &str,
        text: &str,
        offset: usize,
        limit: usize,
    ) -> Result<(Vec<graph_core::model::Link>, usize, Vec<graph_core::model::TypeCount>), GraphError> {
        if refs.is_empty() {
            return Ok((Vec::new(), 0, Vec::new()));
        }
        let counts: std::collections::BTreeMap<String, usize> = self
            .rows(self.sql.link_type_counts(refs, text))
            .await?
            .iter()
            .map(|r| (r.text(0), r.int(1) as usize))
            .collect();
        let (types, total) = graph_core::page::type_counts(&counts, typ);
        let links = self.links(self.sql.link_page(refs, typ, text, offset, limit)).await?;
        Ok((links, total, types))
    }
}

impl Session {
    /// The node versions derived from a version (`Tx.DerivedNodes`).
    pub async fn derived_nodes(
        &self,
        r: &graph_core::model::NodeRef,
    ) -> Result<Vec<graph_core::model::Node>, GraphError> {
        self.nodes(self.sql.derived_nodes(r)).await
    }
}

impl Session {
    /// The node of each impact of a change in the order written, with the version it starts from (`pre`) and
    /// whether it names one after (`post`): the projection Go's `Tx.Change` reads.
    pub async fn impact_nodes(
        &self,
        change: &graph_core::model::ChangeId,
    ) -> Result<Vec<(graph_core::model::NodeId, Option<i32>, bool)>, GraphError> {
        if self.sql.pg && !super::is_uuid(change.as_str()) {
            return Ok(Vec::new());
        }
        let mut out = Vec::new();
        for r in self.rows(self.sql.impact_refs(change)).await? {
            let node = r.text(0);
            if node.is_empty() {
                continue;
            }
            let pre = (!r.is_null(1)).then(|| r.int32(1));
            out.push((graph_core::model::NodeId(node), pre, !r.is_null(2)));
        }
        Ok(out)
    }
}
