//! What going from one baseline to another changes, node by node.

use goap_ctx::Ctx;
use goap_domain::{BaselineID, Node, NodeID, NodeRef, Version, namespace_of};

use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// Kinds of a baseline difference.
pub const DIFF_ADDED: &str = "added";
pub const DIFF_REMOVED: &str = "removed";
pub const DIFF_CHANGED: &str = "changed";

/// BaselineDiff is a node whose version differs between two baselines: added (only in the second), removed (only in
/// the first) or changed (another version). From / To are the node as each baseline holds it (None where absent).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BaselineDiff {
    pub node: NodeID,
    pub key: String,
    pub r#type: String,
    pub kind: String,
    pub from: Option<Node>,
    pub to: Option<Node>,
}

impl Graph {
    /// DiffBaselines compares two baselines of a namespace node by node: what going from the first to the second
    /// changes. Nodes are ordered by key.
    pub async fn diff_baselines(
        &self,
        ctx: &Ctx,
        from: &BaselineID,
        to: &BaselineID,
    ) -> Result<Vec<BaselineDiff>> {
        let mut tx = self.begin(ctx).await?;
        let a = tx.baseline(ctx, from).await?;
        let b = tx.baseline(ctx, to).await?;
        if namespace_of(&a.namespace) != namespace_of(&b.namespace) {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "baselines {from} and {to} are of namespaces {} and {}",
                    a.namespace, b.namespace
                ),
            ));
        }
        async fn node(ctx: &Ctx, tx: &mut dyn Tx, id: &NodeID, v: Version) -> Result<Node> {
            tx.node(
                ctx,
                &NodeRef {
                    id: id.clone(),
                    version: v,
                },
            )
            .await
        }
        let mut out = Vec::new();
        for (id, va) in &a.nodes {
            let vb = b.nodes.get(id);
            if vb == Some(va) {
                continue;
            }
            let mut d = BaselineDiff {
                node: id.clone(),
                kind: DIFF_REMOVED.into(),
                from: Some(node(ctx, &mut tx, id, *va).await?),
                ..Default::default()
            };
            if let Some(vb) = vb {
                d.kind = DIFF_CHANGED.into();
                d.to = Some(node(ctx, &mut tx, id, *vb).await?);
            }
            out.push(d);
        }
        for (id, vb) in &b.nodes {
            if a.nodes.contains_key(id) {
                continue;
            }
            out.push(BaselineDiff {
                node: id.clone(),
                kind: DIFF_ADDED.into(),
                to: Some(node(ctx, &mut tx, id, *vb).await?),
                ..Default::default()
            });
        }
        tx.commit(ctx).await?;
        for d in &mut out {
            let n = d.to.as_ref().or(d.from.as_ref()).expect("one end is set");
            d.key = n.key.clone();
            d.r#type = n.r#type.clone();
        }
        out.sort_by(|x, y| x.key.cmp(&y.key));
        Ok(out)
    }
}
