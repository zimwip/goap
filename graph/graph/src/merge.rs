//! Part of `merge.go` (G1): reading branches and their heads. The merge itself (`planMerge`, the 3-way merge, the
//! resolutions) belongs to a later slice (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{
    BRANCH_OPEN, Baseline, Branch, MAIN_BRANCH, Node, NodeID, branch_of, namespace_of,
};

use crate::repo::Tx;
use crate::{Graph, Result};

impl Graph {
    /// BranchHead returns the latest baseline of a branch of a namespace; the head of main before any change landed
    /// is the empty state (empty id, no nodes).
    pub async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline> {
        let mut tx = self.begin(ctx).await?;
        let b = branch_head(ctx, &mut tx, namespace, name).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    /// Versions returns every version of a node across branches (version graph).
    pub async fn versions(&self, ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>> {
        let mut tx = self.begin(ctx).await?;
        let vs = tx.versions(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(vs)
    }
}

/// branchOf returns a branch of a namespace; main exists implicitly.
pub(crate) async fn branch_of_tx(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    name: &str,
) -> Result<Branch> {
    let (namespace, name) = (namespace_of(namespace), branch_of(name));
    match tx.branch(ctx, namespace, name).await {
        Err(e) if e.is_not_found() && name == MAIN_BRANCH => {
            let head = match branch_head(ctx, tx, namespace, name).await {
                Ok(h) => h,
                Err(e) if e.is_not_found() => Baseline::default(),
                Err(e) => return Err(e),
            };
            Ok(Branch {
                name: name.to_string(),
                namespace: namespace.to_string(),
                head: head.id,
                status: BRANCH_OPEN.to_string(),
                ..Default::default()
            })
        }
        r => r,
    }
}

/// branchHead returns the head baseline of a branch: its head, else (main without applied changes) the latest
/// baseline of main, else the empty state (ADR 0056), the empty baseline id.
pub(crate) async fn branch_head(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    name: &str,
) -> Result<Baseline> {
    let (namespace, name) = (namespace_of(namespace), branch_of(name));
    match tx.branch(ctx, namespace, name).await {
        Ok(b) if !b.head.is_empty() => return tx.baseline(ctx, &b.head).await,
        Ok(_) => {}
        Err(e) if !e.is_not_found() || name != MAIN_BRANCH => return Err(e),
        Err(_) => {}
    }
    // main without applied changes: its latest baseline, if any change left one
    let bs = tx.baselines(ctx, namespace).await?;
    for b in bs.into_iter().rev() {
        if branch_of(&b.branch) == name {
            return Ok(b);
        }
    }
    Ok(Baseline {
        namespace: namespace.to_string(),
        branch: name.to_string(),
        ..Default::default()
    })
}

/// onBranch reports whether a node version is part of a branch: written there, or joined (ADR 0032).
pub(crate) async fn on_branch(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    r: &goap_domain::NodeRef,
    branch: &str,
) -> Result<bool> {
    let vs = tx.versions(ctx, &r.id).await?;
    Ok(vs
        .iter()
        .find(|n| n.version == r.version)
        .is_some_and(|n| n.on(branch)))
}
