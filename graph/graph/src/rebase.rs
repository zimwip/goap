//! Part of `rebase.go` (G2): the stored version seen as a draft, and the conflicts a rebase leaves on an impact
//! (ADR 0082 §2, ADR 0101 §2). The rebase itself (`RebaseChange`, `rebaseTx`, `RebaseState`) belongs to slice G3 (see
//! PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{
    Change, ChangeID, ChangeImpact, ChangeImpactID, Draft, DraftLink, MAIN_FLOW, NodeRef,
    pending_conflicts,
};
use goap_operations as operations;
use serde_json::{Map, Value};

use crate::changelog::impact_events;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// storedDraft is a stored version seen as a draft, with its outgoing links: what a three-way merge compares.
    pub(crate) async fn stored_draft(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        r: &NodeRef,
    ) -> Result<Draft> {
        let n = tx.node(ctx, r).await?;
        let links = tx.out_links(ctx, &n.r#ref()).await?;
        Ok(Draft {
            node: n.id.clone(),
            key: n.key.clone(),
            r#type: n.r#type.clone(),
            state: n.state.clone(),
            owner: n.owner.clone(),
            properties: n.properties.clone(),
            base: Some(n.r#ref()),
            links: links
                .into_iter()
                .map(|l| DraftLink {
                    id: l.id,
                    r#type: l.r#type,
                    to: l.to,
                    properties: l.properties,
                })
                .collect(),
            ..Default::default()
        })
    }

    /// ImpactNodeResolve settles the conflicts a rebase left on a change impact by keeping the change's values (ADR
    /// 0082 §2): an updated event with {"resolved": true}. The impact is then reviewed as any.
    ///
    /// pending goap:operation impact.resolve doer
    pub async fn impact_node_resolve(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        execution: &str,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_RESOLVE).await?;
        let mut tx = self.begin(ctx).await?;
        let mut w = self.work_on(ctx, &mut tx, id, MAIN_FLOW).await?;
        self.impact(ctx, &mut tx, &mut w, impact).await?;
        let d = self.working(ctx, &mut tx, &w).await?;
        let events = impact_events(ctx, &mut tx, id).await?;
        if pending_conflicts(&events, impact).is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change impact {impact} ({}) has no conflict to settle",
                    w.cn.key
                ),
            ));
        }
        let r = d.r#ref();
        let mut patch = Map::new();
        patch.insert("resolved".into(), Value::Bool(true));
        self.emit_updated(ctx, &mut tx, &mut w, &r, execution, patch)
            .await?;
        let cn = w.seen_as(Some(&r));
        tx.commit(ctx).await?;
        Ok(cn)
    }
}

/// checkSettled refuses to accept a change impact whose conflicts from a rebase are not settled (ADR 0082 §2, ADR 0101
/// §2).
pub(crate) async fn check_settled(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    c: &Change,
    impact: &ChangeImpactID,
) -> Result<()> {
    let events = impact_events(ctx, tx, &c.id).await?;
    let pending = pending_conflicts(&events, impact);
    if !pending.is_empty() {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!(
                "change impact {impact} has conflicts with the branch it was forked from to settle first (edit the fields, or keep them with ImpactNodeResolve): {}",
                pending.join(", ")
            ),
        ));
    }
    Ok(())
}

// ---- from `rebase_committed.go` (G2): what holds the merge of a change. The rebase of a committed branch
// (`rebaseCommittedTx`) and the merge itself are slice G3's. ----------------------------------------------------

/// hasCommits reports whether a change committed on its own branch (ADR 0102).
pub(crate) fn has_commits(c: &Change) -> bool {
    !c.result_baseline_id.is_empty()
        && (c.status == goap_domain::CHANGE_DRAFT || c.status == goap_domain::CHANGE_ACTIVE)
}

impl Graph {
    /// withdrawnSince lists the nodes the branch of a change changed since its fork that no impact of its main flow
    /// holds any more (withdrawn after their commit, ADR 0102), sorted, with the state of the fork.
    pub(crate) async fn withdrawn_since(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<(
        Vec<goap_domain::NodeID>,
        std::collections::BTreeMap<goap_domain::NodeID, goap_domain::Version>,
    )> {
        let Some(own) = crate::changebranch::own_branch(ctx, tx, c).await? else {
            return Ok((Vec::new(), Default::default()));
        };
        let fork = tx.baseline(ctx, &own.fork_baseline).await?;
        let head = crate::merge::branch_head(ctx, tx, &c.namespace, &own.name).await?;
        let held: std::collections::HashSet<goap_domain::NodeID> =
            crate::mergecheck::main_impacts_of(c)
                .iter()
                .map(crate::flownodes::node_of)
                .collect();
        let out = head
            .nodes
            .iter()
            .filter(|(id, v)| !held.contains(*id) && fork.nodes.get(*id) != Some(*v))
            .map(|(id, _)| id.clone())
            .collect();
        Ok((out, fork.nodes))
    }

    /// unsettled lists what holds the merge of a change (ADR 0102), one rule for every impact of its main flow: a
    /// rejected impact is to be reworked or withdrawn; a candidate (it modifies nothing and committed nothing) to be
    /// modified or withdrawn; a modification to be committed. An impact a merge or a split comes with (via) is settled
    /// with the impact of its parent. Then the open flows, decision points and sub-changes, and the withdrawals not
    /// committed yet.
    pub(crate) async fn unsettled(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<Vec<String>> {
        let mut out = Vec::new();
        if let Some(f) = crate::flow::open_flows(c).first() {
            out.push(format!("open flow {} (adopt or discard it)", f.id));
        }
        let pd = crate::decision::pending_decisions(c, self.now(), self.decision_policy.as_deref());
        if let Some(p) = pd.first() {
            out.push(format!("pending decision point {:?}", p.question));
        }
        let open = crate::subchange::open_sub_changes(ctx, tx, &c.id).await?;
        if !open.is_empty() {
            out.push(format!(
                "{} open sub-change(s) (merge or abandon them)",
                open.len()
            ));
        }
        let rows = self.drafts(ctx, tx, &c.id).await?;
        for cn in crate::mergecheck::main_impacts_of(c) {
            if !cn.via.is_empty() {
                continue;
            }
            let d = crate::draft::own_draft(&rows, &cn.id, "");
            let modifies = self.draft_modifies(ctx, tx, d.as_ref()).await?;
            if cn.review == goap_domain::REVIEW_REJECTED {
                out.push(format!("{} is rejected (rework or withdraw it)", cn.key));
            } else if crate::checkout::planned_creation(&cn) {
                out.push(format!(
                    "{} is planned, not created yet (create or withdraw it)",
                    cn.key
                ));
            } else if !modifies && cn.landed.is_none() {
                out.push(format!(
                    "{} modifies nothing (modify or withdraw it)",
                    cn.key
                ));
            } else if modifies && cn.review != goap_domain::REVIEW_ACCEPTED {
                out.push(format!("{} awaits its review", cn.key));
            } else if modifies {
                out.push(format!("{} is not committed", cn.key));
            }
        }
        let (w, _) = self.withdrawn_since(ctx, tx, c).await?;
        if !w.is_empty() {
            out.push(format!(
                "{} withdrawn node(s) still on the branch (commit to revert them)",
                w.len()
            ));
        }
        Ok(out)
    }
}
