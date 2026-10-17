//! The graph-wide checks (NodeValidator, ADR 0048) of the state a change leaves.

use std::collections::BTreeMap;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{Link, Node, NodeID, NodeRef, Version, namespace_of};
use goap_graphapi::{ValidatedNode, ValidatorQuery};
use tokio::sync::Mutex;

use crate::apply::Cpost;
use crate::repo::Tx;
use crate::{Graph, Result};

/// txQuery implements ValidatorQuery against the resulting node set of a change being applied (target: the map
/// Apply is about to persist as the new baseline's nodes, ADR 0024), not a branch read: an impacted node's new
/// version only joins its branch once Apply advances it, after validators run. The transaction is behind a mutex
/// (the query's methods take `&self`).
pub(crate) struct TxQuery<'a> {
    tx: Mutex<&'a mut dyn Tx>,
    target: &'a BTreeMap<NodeID, Version>,
}

#[async_trait]
impl ValidatorQuery for TxQuery<'_> {
    async fn nodes_of_type(&self, ctx: &Ctx, namespace: &str, typ: &str) -> Result<Vec<Node>> {
        let ns = namespace_of(namespace);
        let mut tx = self.tx.lock().await;
        let mut out = Vec::new();
        for (id, v) in self.target {
            let n = tx
                .node(
                    ctx,
                    &NodeRef {
                        id: id.clone(),
                        version: *v,
                    },
                )
                .await?;
            if n.namespace == ns && n.r#type == typ && !n.deleted {
                out.push(n);
            }
        }
        Ok(out)
    }

    async fn out_links_of(&self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        self.tx.lock().await.out_links(ctx, r).await
    }

    async fn node(&self, ctx: &Ctx, r: &NodeRef) -> Result<Node> {
        let mut r = r.clone();
        if r.version == 0 {
            r.version = self.target.get(&r.id).copied().unwrap_or(0);
        }
        self.tx.lock().await.node(ctx, &r).await
    }
}

impl Graph {
    /// runNodeValidators runs the registered NodeValidator plugins whose declared types intersect the types this
    /// change actually wrote (ADR 0048), grouping cposts per validator so each sees only its own impacted nodes.
    pub(crate) async fn run_node_validators(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        target: &BTreeMap<NodeID, Version>,
        cposts: &[Cpost],
    ) -> Result<()> {
        if self.validators.is_empty() {
            return Ok(());
        }
        let q = TxQuery {
            tx: Mutex::new(tx),
            target,
        };
        for v in &self.validators {
            let types = v.types();
            let impacted: Vec<ValidatedNode> = cposts
                .iter()
                .filter(|cp| types.contains(&cp.post.r#type))
                .map(|cp| ValidatedNode {
                    post: cp.post.clone(),
                    pre: cp.pre.clone(),
                })
                .collect();
            if impacted.is_empty() {
                continue;
            }
            v.validate(ctx, &q, &impacted).await?;
        }
        Ok(())
    }
}
