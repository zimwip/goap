//! Port of `changebranch.go`: the branch a change owns, and how a change is integrated into the branch it was forked
//! from (a fast-forward, else the three-way merge of merge.rs).

use std::collections::{BTreeMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_FAST_FORWARD, BRANCH_MERGED, BRANCH_OPEN, Baseline, BaselineID, Branch, CHANGE_ACTIVE,
    CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, Change, ChangeID, INTENT_DERIVE, MAIN_BRANCH,
    Node, NodeID, NodeRef, branch_of, change_branch_origin, namespace_of,
};
use goap_operations as operations;
use serde::{Deserialize, Serialize};

use crate::api::{MergePlan, Resolution};
use crate::merge::{MergeRequest, branch_head, branch_of_tx, plan_merge};
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// SharedNode is a node several open changes act on: a merge is needed.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SharedNode {
    pub node: NodeRef,
    pub key: String,
    pub changes: Vec<ChangeID>,
}

/// unresolved lists the keys of the conflicting candidates of a plan no resolution settles.
pub(crate) fn unresolved(p: &MergePlan, resolutions: &BTreeMap<NodeID, Resolution>) -> Vec<String> {
    p.conflicting()
        .into_iter()
        .filter(|c| !resolutions.contains_key(&c.node))
        .map(|c| c.key)
        .collect()
}

/// changeBranchName names the branch owned by a change.
pub(crate) fn change_branch_name(id: &ChangeID) -> String {
    let s = id.as_str();
    let s = if s.len() > 8 {
        s.get(..8).unwrap_or(s)
    } else {
        s
    };
    format!("change-{s}")
}

/// ownBranch returns the branch owned by the change, when it has one.
pub(crate) async fn own_branch(ctx: &Ctx, tx: &mut dyn Tx, c: &Change) -> Result<Option<Branch>> {
    if branch_of(&c.branch) == MAIN_BRANCH {
        return Ok(None);
    }
    let b = tx.branch(ctx, &c.namespace, &c.branch).await?;
    if b.origin == change_branch_origin(&c.id) {
        Ok(Some(b))
    } else {
        Ok(None)
    }
}

impl Graph {
    /// ensureOwnBranch gives a change the branch of its own that the versions it writes live on (ADR 0024), forked
    /// from its reference baseline like the one opened with the change. The items of a change are only applied when
    /// it is, so nothing is on the branch it acted on yet.
    pub(crate) async fn ensure_own_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        mut c: Change,
    ) -> Result<(Change, Branch)> {
        if !c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change {} is a sub-change without a branch of its own",
                    c.id
                ),
            ));
        }
        let parent = branch_of_tx(ctx, tx, &c.namespace, &c.branch).await?;
        if parent.status != BRANCH_OPEN {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("branch {} is {}", parent.name, parent.status),
            ));
        }
        let fork = tx.baseline(ctx, &c.baseline_id).await?;
        let own = Branch {
            name: change_branch_name(&c.id),
            namespace: c.namespace.clone(),
            parent: parent.name.clone(),
            fork_baseline: fork.id.clone(),
            head: fork.id,
            origin: change_branch_origin(&c.id),
            intent: INTENT_DERIVE,
            status: BRANCH_OPEN.to_string(),
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_branch(ctx, own.clone()).await?;
        c.branch = own.name.clone();
        tx.put_change(ctx, c.clone()).await?;
        Ok((c, own))
    }

    /// integrateTx integrates a committed change into the branch it was forked from (its own branch is the one to
    /// merge). Unresolved conflicts leave a change committed. (Go's callers are its tests: so are the Rust ones.)
    #[cfg(test)]
    pub(crate) async fn integrate_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: Change,
        resolutions: &BTreeMap<NodeID, Resolution>,
    ) -> Result<Change> {
        let Some(own) = own_branch(ctx, tx, &c).await? else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {} has no branch of its own", c.id),
            ));
        };
        Box::pin(self.integrate(ctx, tx, c, own, resolutions)).await
    }

    /// integrate merges the branch of a change into its parent branch and marks the change applied. Unresolved
    /// conflicts leave the change committed: its integration waits for IntegrateChange.
    pub(crate) async fn integrate(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        mut c: Change,
        own: Branch,
        resolutions: &BTreeMap<NodeID, Resolution>,
    ) -> Result<Change> {
        if self.fast_forward(ctx, tx, &c, own.clone()).await? {
            return tx.change(ctx, &c.id).await;
        }
        let plan = plan_merge(ctx, tx, &c.namespace, &own.name, &own.parent).await?;
        for cand in plan.conflicting() {
            if !resolutions.contains_key(&cand.node) {
                // the merge waits for a resolution: the change is committed, frozen until it is integrated
                c.status = CHANGE_COMMITTED;
                tx.put_change(ctx, c.clone()).await?;
                return Ok(c);
            }
        }
        let res = self
            .merge_branch_tx(
                ctx,
                tx,
                MergeRequest {
                    from: own.name.clone(),
                    into: own.parent.clone(),
                    title: format!("merge {}", c.title),
                    namespace: c.namespace.clone(),
                    resolutions: resolutions.clone(),
                    owner_org: c.owner_org.clone(),
                    project_id: c.project_id.clone(),
                },
            )
            .await?;
        c.status = CHANGE_APPLIED;
        c.result_baseline_id = res.baseline.id.clone();
        self.land_impacts(ctx, tx, &c, &own, &res.change.id, &res.baseline.id)
            .await?;
        tx.put_change(ctx, c.clone()).await?;
        Ok(c)
    }

    /// IntegrateChange merges a change into what it was forked from (ADR 0102, merge): a root change that committed on
    /// its own branch into the branch it was forked from, a sub-change into its parent. A root change whose three-way
    /// merge waits for its resolutions (committed) is integrated with them.
    ///
    /// goap:operation change.merge doer
    pub async fn integrate_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: &BTreeMap<NodeID, Resolution>,
    ) -> Result<Change> {
        self.allow(ctx, id, operations::CHANGE_MERGE).await?;
        let c = self.change(ctx, id).await?;
        if c.status == CHANGE_DRAFT || c.status == CHANGE_ACTIVE {
            return Box::pin(self.merge(ctx, id, resolutions)).await;
        }
        if c.status != CHANGE_COMMITTED || !c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}", c.status),
            ));
        }
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        if c.status != CHANGE_COMMITTED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}, not committed", c.status),
            ));
        }
        let Some(own) = own_branch(ctx, &mut tx, &c).await? else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {id} has no branch of its own"),
            ));
        };
        tx.lock_branch(ctx, &c.namespace, branch_of(&own.parent))
            .await?;
        let plan = plan_merge(ctx, &mut tx, &c.namespace, &own.name, &own.parent).await?;
        let un = unresolved(&plan, resolutions);
        if !un.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "merge {} into {}: unresolved conflicts on [{}]",
                    own.name,
                    own.parent,
                    un.join(" ")
                ),
            ));
        }
        let c = Box::pin(self.integrate(ctx, &mut tx, c, own, resolutions)).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// SharedNodes lists the nodes of a change that other unapplied changes of the same namespace act on as well.
    pub async fn shared_nodes(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<SharedNode>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let open = self.open_change_holders(ctx, &mut tx).await?;
        let mut seen = HashSet::new();
        let mut out = Vec::new();
        for cn in &c.nodes {
            let Some(pre) = &cn.pre else { continue };
            if !seen.insert(pre.id.clone()) {
                continue;
            }
            let mut others = Vec::new();
            for o in open.get(&pre.id).into_iter().flatten() {
                if o == id {
                    continue;
                }
                let oc = tx.change(ctx, o).await?;
                if namespace_of(&oc.namespace) != namespace_of(&c.namespace) {
                    continue;
                }
                others.push(o.clone());
            }
            if others.is_empty() {
                continue;
            }
            let n = tx.node(ctx, pre).await?;
            out.push(SharedNode {
                node: pre.clone(),
                key: n.key,
                changes: others,
            });
        }
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// NodeByKeyOn returns the latest version of the node with the given key as seen from a branch: the branch's own
    /// version, else its parent's, up to main.
    pub async fn node_by_key_on(
        &self,
        ctx: &Ctx,
        namespace: &str,
        branch: &str,
        key: &str,
    ) -> Result<Node> {
        let mut tx = self.begin(ctx).await?;
        let n = node_by_key_on(ctx, &mut tx, namespace, branch, key).await?;
        tx.commit(ctx).await?;
        Ok(n)
    }

    /// fastForward lands a change whose target branch has not moved since its branch was forked: the versions of the
    /// branch join the target (ADR 0032), no merge version is made, and each change impact lands as the version it
    /// wrote.
    pub(crate) async fn fast_forward(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        mut own: Branch,
    ) -> Result<bool> {
        let into = branch_head(ctx, tx, &c.namespace, &own.parent).await?;
        if into.id != own.fork_baseline {
            return Ok(false);
        }
        let from = branch_head(ctx, tx, &c.namespace, &own.name).await?;
        // what the branch changed since its fork joins the target: the versions of its head baseline (a rejected or
        // replaced version stays behind), and the tombstones of the nodes it retired
        let target = branch_of(&own.parent).to_string();
        let mut moves = Vec::new();
        for (id, v) in &from.nodes {
            if into.nodes.get(id) != Some(v) {
                moves.push(NodeRef {
                    id: id.clone(),
                    version: *v,
                });
            }
        }
        for id in into.nodes.keys() {
            if from.nodes.contains_key(id) {
                continue;
            }
            match tx.latest_on(ctx, id, &own.name).await {
                Ok(n) if n.deleted => moves.push(n.r#ref()),
                Ok(_) => {}
                Err(e) if e.is_not_found() => {}
                Err(e) => return Err(e),
            }
        }
        for r in &moves {
            tx.join_branch(ctx, r, &target, &c.id).await?;
        }
        // a fast-forward, not a merge (own.parent did not move since the fork): name it after the change, not "merge …"
        let res = Baseline {
            id: BaselineID::new(self.new_id()),
            name: c.title.clone(),
            namespace: c.namespace.clone(),
            branch: target.clone(),
            parent_id: into.id.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_FAST_FORWARD.into(),
            nodes: from.nodes,
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_baseline(ctx, res.clone()).await?;
        self.advance_branch(ctx, tx, &c.namespace, &target, &res)
            .await?;
        own.status = BRANCH_MERGED.to_string();
        tx.put_branch(ctx, own.clone()).await?;
        let mut c = c.clone();
        c.status = CHANGE_APPLIED;
        c.result_baseline_id = res.id.clone();
        self.land_impacts(ctx, tx, &c, &own, &c.id, &res.id).await?;
        tx.put_change(ctx, c).await?;
        Ok(true)
    }
}

/// nodeByKeyOn (inside a transaction): the latest version of the node with the key as seen from a branch.
pub(crate) async fn node_by_key_on(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    branch: &str,
    key: &str,
) -> Result<Node> {
    let id = tx.node_id_by_key(ctx, namespace, key).await?;
    let mut name = branch_of(branch).to_string();
    for _ in 0..64 {
        match tx.latest_on(ctx, &id, &name).await {
            Ok(n) => return Ok(n),
            Err(e) if !e.is_not_found() => return Err(e),
            Err(_) => {}
        }
        if name == MAIN_BRANCH {
            break;
        }
        let b = tx.branch(ctx, namespace, &name).await?;
        name = branch_of(&b.parent).to_string();
    }
    Err(Error::wrap(
        ErrorKind::NotFound,
        format!("node key {key:?} on branch {branch}"),
    ))
}
