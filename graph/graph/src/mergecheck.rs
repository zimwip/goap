//! Port of `mergecheck.go`: the merge of a change is held by its guardian (ADR 0102), the same as its transitions
//! (ADR 0058): the lifecycle of the change must be in a final state, then the methodology has its say
//! (`ChangeLifecycles::merge`: the goal of the Activity the change is scoped to), else the landable states of the
//! domain decide (ADR 0078); then the outer guardians of its chain, innermost first (ADR 0106). Before them, the
//! changes it depends on must have reached the branch it merges into (ADR 0108). Asked outside any transaction: the
//! guardian may read the graph. The merge that asks it (`merge`, `IntegrateChange`) is slice G3's.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    Blackboard, Change, ChangeID, ChangeImpact, FACET_DEPENDENCIES, NodeRef, NodeView,
};

use crate::changebranch::own_branch;
use crate::changeimpact_apply::join_sorted;
use crate::dependency::{check_dependencies, dependencies_of};
use crate::draft::view_in;
use crate::flownodes::node_of;
use crate::lifecycle::invalidf;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// checkMerge asks the guardian of a change whether it may merge.
    pub(crate) async fn check_merge(&self, ctx: &Ctx, id: &ChangeID) -> Result<()> {
        let (c, bb, landed, logs, deps) = {
            let mut tx = self.begin(ctx).await?;
            let c = tx.change(ctx, id).await?;
            let logs = self.log_entries(ctx, &mut tx, id).await?;
            let deps = dependencies_of(ctx, &mut tx, &c).await?;
            let (bb, landed) = self.merge_blackboard(ctx, &mut tx, &c).await?;
            let bb = bb.with_facet(FACET_DEPENDENCIES, deps.clone());
            tx.commit(ctx).await?;
            (c, bb, landed, logs, deps)
        };
        // the facets folded from the log (the history of the change, ADR 0109), outside the transaction
        let bb = self.with_log_facets(ctx, bb, &logs).await?;
        // first the changes it depends on (ADR 0108): their work has reached the branch it merges into
        check_dependencies(&c, &deps)?;
        if !c.lifecycle.is_empty() {
            let lc = self.change_lifecycle(ctx, &c).await?;
            let fin = lc
                .as_ref()
                .and_then(|lc| lc.state(&c.state))
                .is_some_and(|s| s.r#final);
            if !fin {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "change {} is in state {} of lifecycle {}: it is merged once it is in a final state",
                        c.id, c.state, c.lifecycle
                    ),
                ));
            }
        }
        self.own_guardian_merge(ctx, &c, &bb, &landed).await?;
        // then the outer guardians of its chain (ADR 0106), innermost first
        self.check_guardians(ctx, &bb, "").await
    }

    /// ownGuardianMerge is the say of the change's own methodology on its merge: its Activity goal, else the landable
    /// states.
    pub(crate) async fn own_guardian_merge(
        &self,
        ctx: &Ctx,
        c: &Change,
        bb: &Blackboard,
        landed: &[NodeView],
    ) -> Result<()> {
        if let Some(lcs) = &self.lifecycles {
            let (decided, ok) = lcs.merge(ctx, c, bb).await?;
            if decided && !ok {
                return Err(invalidf(
                    "the change does not satisfy the goal of its methodology",
                ));
            }
            if decided {
                return Ok(());
            }
        }
        let stuck: Vec<String> = landed
            .iter()
            .filter(|n| n.not_landable)
            .map(|n| format!("{} ({}) in {}", n.node.key, n.node.r#type, n.node.state))
            .collect();
        if !stuck.is_empty() {
            return Err(invalidf(format!(
                "the change leaves nodes in a state that cannot land, move them to a landable state first: {}",
                join_sorted(&stuck)
            )));
        }
        Ok(())
    }

    /// mergeBlackboard is what the guardian judges: the change with, for each impact, its pre version and what it
    /// lands (the version on the change's branch), those landed nodes returned apart.
    pub(crate) async fn merge_blackboard(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<(Blackboard, Vec<NodeView>)> {
        let mut bb = Blackboard {
            change: c.clone(),
            ..Default::default()
        };
        let ix = self.types_at();
        let impacts = main_impacts_of(c);
        let r = self.new_draft_reader(ctx, tx, c, "", &impacts).await?;
        let mut head = BTreeMap::new();
        if let Some(own) = own_branch(ctx, tx, c).await? {
            head = branch_head(ctx, tx, &c.namespace, &own.name).await?.nodes;
        }
        let mut landed = Vec::new();
        for cn in &impacts {
            if let Some(pre) = &cn.pre {
                let v = view(ctx, tx, &ix, &r, pre).await?;
                bb.nodes.insert(pre.clone(), v);
            }
            let id = node_of(cn);
            let Some(at) = head.get(&id) else { continue };
            if cn.landed.is_none() {
                continue;
            }
            let to = NodeRef {
                id: id.clone(),
                version: *at,
            };
            let v = view(ctx, tx, &ix, &r, &to).await?;
            bb.nodes.insert(to, v.clone());
            if let Some(post) = &cn.post {
                bb.nodes.insert(post.clone(), v.clone()); // the impact's post reads what it lands
            }
            landed.push(v);
        }
        Ok((bb, landed))
    }
}

async fn view(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    ix: &crate::lifecycle::TypeIndex,
    r: &crate::draft::DraftReader,
    at: &NodeRef,
) -> Result<NodeView> {
    let mut v = view_in(ctx, tx, r, at).await?;
    if let Some(lc) = ix.lifecycle_of(&v.node.r#type)
        && !v.node.state.is_empty()
    {
        v.not_landable = !lc.landable(&v.node.state);
    }
    Ok(v)
}

/// mainImpactsOf are the impacts of the main flow of a change: the ones its commit and its merge look at.
pub(crate) fn main_impacts_of(c: &Change) -> Vec<ChangeImpact> {
    c.nodes
        .iter()
        .filter(|cn| cn.flow.is_empty() && !cn.superseded)
        .cloned()
        .collect()
}
