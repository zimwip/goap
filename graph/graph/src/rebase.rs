//! Port of `rebase.go` and `rebase_committed.go`. Rebasing a change (ADR 0101, 0102, 0103): what it committed on its
//! branch is brought onto the head of the branch it was forked from (main, a release branch, the branch of its parent
//! change), the nodes changed on both sides merged three-way (`merge_drafts`, `rebase_committed_tx`); the fields changed
//! on both sides are conflicts the change settles by editing its draft, and its merge is then a fast-forward. A change
//! that committed nothing has nothing to rebase.
//!
//! Iterating on the branch of a change and merging it (ADR 0102): committing writes its versions on its own branch and
//! leaves it open; merging takes the branch into the one it was forked from, once every impact of it is settled, and
//! closes it. When the target moved since the fork, the merge first rebases the branch onto its head; the merge is then
//! a fast-forward. A sub-change is merged into its parent the same way: settled, rebased onto its parent, asked to the
//! merge guards, then integrated.

use std::collections::{BTreeMap, HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_REBASE, Baseline, BaselineID, Branch, CHANGE_ABANDONED, CHANGE_APPLIED, CONFLICT_NODE,
    Change, ChangeID, ChangeImpact, ChangeImpactID, Draft, DraftLink, HeaderEdit, HeaderValue,
    IMPACT_CREATED, IMPACT_LANDED, IMPACT_PROPOSED, IMPACT_REBASED, IMPACT_REVIEWED,
    IMPACT_TRANSITIONED, ImpactEvent, ItemID, LinkID, MAIN_FLOW, NodeID, NodeRef, REVIEW_ACCEPTED,
    REVIEW_OUTCOME_ACCEPT, REVIEW_PROPOSED, Review, ReviewEntry, branch_of, draft_changed,
    header_entry, merge_drafts, pending_conflicts,
};
use goap_operations as operations;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::api::{ImpactConflicts, RebaseResult, RebaseState, RebasedImpact, Resolution};
use crate::changebranch::own_branch;
use crate::changelog::impact_events;
use crate::draft::own_draft;
use crate::flownodes::node_of;
use crate::merge::branch_head;
use crate::mergecheck::main_impacts_of;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// HeaderBaselineID is the field of a change.updated log entry that records the move of a rebase (ADR 0101).
pub const HEADER_BASELINE_ID: &str = "baselineId";

/// CommitState is what a change can commit and merge (ADR 0102): the accepted modifications the next commit writes,
/// the withdrawn nodes it reverts, what holds the merge. The merge guards are asked when the merge is made, not here.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct CommitState {
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub to_commit: Vec<String>,
    #[serde(skip_serializing_if = "is_zero")]
    pub reverts: usize,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub unsettled: Vec<String>,
    pub commits: bool,
    pub can_commit: bool,
    pub can_merge: bool,
}

fn is_zero(n: &usize) -> bool {
    *n == 0
}

impl Graph {
    /// RebaseChange brings what a change committed up to date with the head of the branch it was forked from (the
    /// branch of its parent change for a sub-change, ADR 0103).
    ///
    /// goap:operation change.rebase doer
    pub async fn rebase_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseResult> {
        self.allow(ctx, id, operations::CHANGE_REBASE).await?;
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        let res = self.rebase_tx(ctx, &mut tx, &c).await?;
        tx.commit(ctx).await?;
        Ok(res)
    }

    /// rebaseTx is RebaseChange inside a transaction.
    pub(crate) async fn rebase_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<RebaseResult> {
        if c.status == CHANGE_APPLIED || c.status == CHANGE_ABANDONED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {} is {}", c.id, c.status),
            ));
        }
        let own = own_branch(ctx, tx, c).await?;
        let (Some(own), true) = (own, has_commits(c)) else {
            return Ok(RebaseResult {
                parent: c.parent_id.clone(),
                ..Default::default()
            });
        };
        let mut res = Box::pin(self.rebase_committed_tx(ctx, tx, c, own)).await?;
        res.parent = c.parent_id.clone();
        Ok(res)
    }

    /// numberLinks gives the links a merge brought (they have no id) one.
    pub(crate) fn number_links(&self, d: &mut Draft) {
        for l in &mut d.links {
            if l.id.is_empty() {
                l.id = LinkID::new(self.new_id());
            }
        }
    }

    /// installRebased replaces the draft of a change impact by the merged one (a transitioned event carrying it, with
    /// the rebase and its conflicts in the patch), moves its pre with it when pre is given, and sends its review back
    /// to proposed when asked (the reason is the comment).
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn install_rebased(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        cn: &ChangeImpact,
        d: &Draft,
        merged: Draft,
        pre: Option<NodeRef>,
        rebased: Value,
        conflicts: &[String],
        back_to_proposed: bool,
        why: &str,
    ) -> Result<()> {
        let r = merged.r#ref();
        let mut patch = Map::new();
        patch.insert("rebased".into(), rebased);
        // a Go nil slice: null when there is no conflict
        patch.insert(
            "conflicts".into(),
            if conflicts.is_empty() {
                Value::Null
            } else {
                json!(conflicts)
            },
        );
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: IMPACT_TRANSITIONED,
                execution: d.execution.clone(),
                post: Some(r),
                draft: Some(merged),
                pre,
                patch,
                ..Default::default()
            }],
        )
        .await?;
        if !back_to_proposed || cn.review == REVIEW_PROPOSED {
            return Ok(());
        }
        let r = Review {
            status: REVIEW_PROPOSED,
            by: self.caller_of(ctx),
            at: self.now(),
            comment: why.to_string(),
            ..Default::default()
        };
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: IMPACT_REVIEWED,
                by: r.by.clone(),
                review: Some(r),
                ..Default::default()
            }],
        )
        .await
    }

    /// RebaseState reads where a change stands against the branch it was forked from. A change that committed nothing,
    /// or a closed one, has nothing behind.
    pub async fn rebase_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<RebaseState> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let mut st = RebaseState {
            parent: c.parent_id.clone(),
            ..Default::default()
        };
        let events = impact_events(ctx, &mut tx, &c.id).await?;
        for cn in &c.nodes {
            let pending = pending_conflicts(&events, &cn.id);
            if !pending.is_empty() {
                st.conflicts.push(ImpactConflicts {
                    impact: cn.id.clone(),
                    key: cn.key.clone(),
                    conflicts: pending,
                });
            }
        }
        if c.status != CHANGE_APPLIED
            && c.status != CHANGE_ABANDONED
            && let Some(own) = own_branch(ctx, &mut tx, &c).await?
            && has_commits(&c)
        {
            self.committed_behind(ctx, &mut tx, &c, &own, &mut st)
                .await?;
        }
        tx.commit(ctx).await?;
        Ok(st)
    }
}

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
    /// goap:operation impact.resolve doer
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

// ---- rebase_committed.go ------------------------------------------------------------------------------------

/// hasCommits reports whether a change committed on its own branch (ADR 0102).
pub(crate) fn has_commits(c: &Change) -> bool {
    !c.result_baseline_id.is_empty()
        && (c.status == goap_domain::CHANGE_DRAFT || c.status == goap_domain::CHANGE_ACTIVE)
}

impl Graph {
    /// uncommitted lists the change impacts whose draft holds a modification not committed yet (ADR 0102).
    pub(crate) async fn uncommitted(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<Vec<String>> {
        let rows = self.drafts(ctx, tx, &c.id).await?;
        let mut out = Vec::new();
        for cn in main_impacts_of(c) {
            if self
                .draft_modifies(ctx, tx, own_draft(&rows, &cn.id, "").as_ref())
                .await?
            {
                out.push(cn.key);
            }
        }
        Ok(out)
    }

    /// merge merges a change into what it was forked from (ADR 0102): settled, brought up to date (a root change: its
    /// branch rebased onto the head of its target; a sub-change: rebased onto its parent), asked to the merge guards,
    /// then integrated (a fast-forward; a target moving in between falls back to the three-way merge with the
    /// resolutions given).
    pub(crate) async fn merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        resolutions: &BTreeMap<NodeID, Resolution>,
    ) -> Result<Change> {
        let c = {
            let mut tx = self.change_tx(ctx, id).await?;
            let c = tx.change(ctx, id).await?;
            let left = self.unsettled(ctx, &mut tx, &c).await?;
            if !left.is_empty() {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change {id} cannot be merged yet: {}", left.join("; ")),
                ));
            }
            if !has_commits(&c) {
                return Err(Error::wrap(
                    ErrorKind::NothingToCommit,
                    format!("change {id} committed nothing on its branch"),
                ));
            }
            self.check_parent_drafts(ctx, &mut tx, &c).await?;
            tx.commit(ctx).await?;
            c
        };
        let res = {
            let mut tx = self.change_tx(ctx, id).await?;
            let res = Box::pin(self.rebase_tx(ctx, &mut tx, &c)).await?;
            tx.commit(ctx).await?;
            res
        };
        if res.changed() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} {}", res.summary()),
            ));
        }
        Box::pin(self.check_merge(ctx, id)).await?;
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        let Some(own) = own_branch(ctx, &mut tx, &c).await? else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {id} has no branch of its own"),
            ));
        };
        // the head of the target is read under its lock: a merge landing meanwhile is seen, and this one merges after
        // it (ADR 0127)
        tx.lock_branch(ctx, &c.namespace, branch_of(&own.parent))
            .await?;
        let c = if c.parent_id.is_empty() {
            Box::pin(self.integrate(ctx, &mut tx, c, own, resolutions)).await?
        } else {
            // a sub-change fast-forwards the branch of its parent, which takes its work as its own (ADR 0103)
            if !self.fast_forward(ctx, &mut tx, &c, own).await? {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "the branch of parent change {} moved meanwhile: merge {id} again",
                        c.parent_id
                    ),
                ));
            }
            let c = tx.change(ctx, id).await?;
            Box::pin(self.record_in_parent(ctx, &mut tx, &c)).await?;
            c
        };
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// checkParentDrafts refuses the merge of a sub-change while its parent holds an uncommitted modification of a
    /// node the sub-change changed: the parent commits or cancels it first (its draft would no longer start from its
    /// branch).
    pub(crate) async fn check_parent_drafts(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<()> {
        if c.parent_id.is_empty() {
            return Ok(());
        }
        let parent = tx.change(ctx, &c.parent_id).await?;
        let rows = self.drafts(ctx, tx, &parent.id).await?;
        let mine: HashSet<NodeID> = main_impacts_of(c).iter().map(node_of).collect();
        let mut held = Vec::new();
        for cn in main_impacts_of(&parent) {
            if !mine.contains(&node_of(&cn)) {
                continue;
            }
            if self
                .draft_modifies(ctx, tx, own_draft(&rows, &cn.id, "").as_ref())
                .await?
            {
                held.push(cn.key);
            }
        }
        if !held.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "parent change {} holds uncommitted modifications of {}: it commits or cancels them first",
                    parent.id,
                    held.join(", ")
                ),
            ));
        }
        Ok(())
    }

    /// recordInParent gives the parent of a merged sub-change an impact for each node the sub-change landed on the
    /// parent's branch (ADR 0103): its own impact of the node, else a new one, accepted with the sub-change's review
    /// and landed as the version the sub-change committed. The parent then holds the work as its own, committed.
    pub(crate) async fn record_in_parent(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<()> {
        let parent = tx.change(ctx, &c.parent_id).await?;
        let head = branch_head(ctx, tx, &parent.namespace, &parent.branch).await?;
        // the parent's acceptances are one review, recorded before them (ADR 0105)
        struct LandedIn {
            events: Vec<ImpactEvent>,
            id: ChangeImpactID,
            r#ref: NodeRef,
            review: Review,
        }
        let mut todo = Vec::new();
        let mut entries = Vec::new();
        let parent_impacts = main_impacts_of(&parent);
        for cn in main_impacts_of(c) {
            let Some(r) = cn.landed.clone() else { continue };
            let mut events = Vec::new();
            let mut id = ChangeImpactID::default();
            for p in &parent_impacts {
                if node_of(p) == r.id {
                    id = p.id.clone();
                }
            }
            if id.is_empty() {
                id = ChangeImpactID::new(self.new_id());
                let mut st = ChangeImpact {
                    id: id.clone(),
                    key: cn.key.clone(),
                    r#type: cn.r#type.clone(),
                    intent: cn.intent.clone(),
                    rationale: cn.rationale.clone(),
                    review: REVIEW_PROPOSED,
                    produced_by: "graph.merge".into(),
                    derived_from: vec![ItemID::new(cn.id.to_string())],
                    execution: cn.execution.clone(),
                    created_at: self.now(),
                    ..Default::default()
                };
                if let Some(p) = &cn.pre {
                    st.pre = Some(p.clone());
                    events.push(ImpactEvent {
                        change: parent.id.clone(),
                        impact: id.clone(),
                        op: IMPACT_PROPOSED,
                        state: Some(st),
                        ..Default::default()
                    });
                } else {
                    events.push(ImpactEvent {
                        change: parent.id.clone(),
                        impact: id.clone(),
                        op: IMPACT_CREATED,
                        state: Some(st),
                        post: Some(r.clone()),
                        ..Default::default()
                    });
                }
            }
            let last = cn
                .reviews
                .iter()
                .rev()
                .find(|r| r.flow.is_empty() && !r.superseded)
                .cloned()
                .unwrap_or_default();
            let review = Review {
                status: REVIEW_ACCEPTED,
                by: last.by.clone(),
                at: self.now(),
                comment: format!(
                    "merged from sub-change {} ({}): {}",
                    c.title, c.id, last.comment
                )
                .trim()
                .to_string(),
                ..Default::default()
            };
            entries.push(ReviewEntry {
                impact: id.to_string(),
                comment: review.comment.clone(),
                outcome: REVIEW_OUTCOME_ACCEPT.to_string(),
            });
            todo.push(LandedIn {
                events,
                id,
                r#ref: r,
                review,
            });
        }
        if !todo.is_empty() {
            let (key, cause) = self
                .put_review_record(
                    ctx,
                    tx,
                    &parent.id,
                    "graph.merge",
                    &format!("merge of sub-change {} ({})", c.title, c.id),
                    entries,
                )
                .await?;
            for t in todo {
                let mut r = t.review;
                r.review_id = key.clone();
                let mut events = t.events;
                events.push(ImpactEvent {
                    change: parent.id.clone(),
                    impact: t.id.clone(),
                    op: IMPACT_REVIEWED,
                    by: r.by.clone(),
                    review: Some(r),
                    causation: cause.clone(),
                    ..Default::default()
                });
                events.push(ImpactEvent {
                    change: parent.id.clone(),
                    impact: t.id.clone(),
                    op: IMPACT_LANDED,
                    landed: Some(t.r#ref),
                    baseline: head.id.clone(),
                    branch: branch_of(&parent.branch).to_string(),
                    ..Default::default()
                });
                self.emit(ctx, tx, events).await?;
            }
        }
        // the parent's branch now holds the sub-change's commits: the parent has committed work to merge
        let mut parent = tx.change(ctx, &parent.id).await?;
        parent.result_baseline_id = head.id;
        tx.put_change(ctx, parent).await
    }

    /// rebaseCommittedTx rebases a change that committed on its own branch onto the head of its target branch: a
    /// baseline of kind rebase on its branch (the head of the target with the versions the change committed; its
    /// parent the previous head of the branch, MergedFrom the head it was rebased onto, RebasedFrom the fork it left),
    /// the fork of the branch and the change's reference baseline moved to that head, and, for each node changed on
    /// both sides since the fork, a draft merging the two (base: the version at the fork; ours: the version the change
    /// committed; theirs: the version on the target), its Base the committed version and MergedWith the target's. A
    /// rebase with no conflict keeps the reviews and commits the merged drafts; a conflict sends its impact back to
    /// proposed, to be settled, accepted and committed.
    pub(crate) async fn rebase_committed_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        mut own: Branch,
    ) -> Result<RebaseResult> {
        let target = branch_of(&own.parent).to_string();
        let mut res = RebaseResult {
            branch: target.clone(),
            from: own.fork_baseline.clone(),
            ..Default::default()
        };
        let head = branch_head(ctx, tx, &c.namespace, &target).await?;
        res.onto = head.id.clone();
        if head.id == own.fork_baseline {
            return Ok(res);
        }
        let keys = self.uncommitted(ctx, tx, c).await?;
        if !keys.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} holds uncommitted modifications ({}): commit or cancel them before it is rebased",
                    c.id,
                    keys.join(", ")
                ),
            ));
        }
        let fork = tx.baseline(ctx, &own.fork_baseline).await?;
        let mine = branch_head(ctx, tx, &c.namespace, &own.name).await?;
        let impact_of: HashMap<NodeID, ChangeImpact> = main_impacts_of(c)
            .into_iter()
            .map(|cn| (node_of(&cn), cn))
            .collect();
        let mut nodes = head.nodes.clone();
        for (id, hv) in &mine.nodes {
            let (hv, fv) = (*hv, fork.nodes.get(id).copied());
            if fv == Some(hv) {
                continue; // the change did not change it: the target's version stands
            }
            nodes.insert(id.clone(), hv);
            let tv = head.nodes.get(id).copied();
            if tv == fv {
                continue; // changed by the change only
            }
            let Some(cn) = impact_of.get(id) else {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "node {id} changed on the branch of change {} by no impact of it",
                        c.id
                    ),
                ));
            };
            let ours_ref = NodeRef {
                id: id.clone(),
                version: hv,
            };
            let ours = self.stored_draft(ctx, tx, &ours_ref).await?;
            let theirs_ref = NodeRef {
                id: id.clone(),
                version: tv.unwrap_or(0),
            };
            let (mut merged, conflicts) = match (fv, tv) {
                (Some(fv), Some(_)) => {
                    let base = self
                        .stored_draft(
                            ctx,
                            tx,
                            &NodeRef {
                                id: id.clone(),
                                version: fv,
                            },
                        )
                        .await?;
                    let theirs = self.stored_draft(ctx, tx, &theirs_ref).await?;
                    let (mut merged, conflicts) = merge_drafts(&base, &ours, &theirs);
                    self.number_links(&mut merged);
                    (merged, conflicts)
                }
                _ => (ours.clone(), vec![CONFLICT_NODE.to_string()]),
            };
            if conflicts.is_empty() && !draft_changed(Some(&ours), &merged) {
                continue; // what the target changed, the change holds already
            }
            for l in &mut merged.links {
                l.id = LinkID::new(self.new_id()); // the links of the version it lands as are its own
            }
            merged.change = c.id.clone();
            merged.impact = cn.id.clone();
            merged.flow = String::new();
            merged.base = Some(ours_ref.clone());
            merged.merged_with = Some(theirs_ref.clone());
            let rebased = json!({"branch": target, "baseline": head.id.to_string()});
            // the change now starts from the target's version
            let pre = (cn.pre.is_some() && tv.is_some()).then(|| theirs_ref.clone());
            self.install_rebased(
                ctx,
                tx,
                c,
                cn,
                &ours,
                merged,
                pre,
                rebased,
                &conflicts,
                !conflicts.is_empty(),
                &format!("rebased onto {target}"),
            )
            .await?;
            res.impacts.push(RebasedImpact {
                impact: cn.id.clone(),
                key: cn.key.clone(),
                changed: !conflicts.is_empty() && cn.review != REVIEW_PROPOSED,
                merged: true,
                conflicts,
            });
        }
        // the impacts that committed nothing start from the target's version of their node too (to check again, ADR
        // 0101)
        for cn in main_impacts_of(c) {
            let (None, Some(pre)) = (&cn.landed, &cn.pre) else {
                continue;
            };
            if let Some(v) = head.nodes.get(&pre.id)
                && *v != pre.version
            {
                self.emit(
                    ctx,
                    tx,
                    vec![ImpactEvent {
                        change: c.id.clone(),
                        impact: cn.id.clone(),
                        op: IMPACT_REBASED,
                        pre: Some(NodeRef {
                            id: pre.id.clone(),
                            version: *v,
                        }),
                        ..Default::default()
                    }],
                )
                .await?;
            }
        }
        let rb = Baseline {
            id: BaselineID::new(self.new_id()),
            name: c.title.clone(),
            namespace: c.namespace.clone(),
            branch: own.name.clone(),
            parent_id: mine.id.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_REBASE.into(),
            nodes,
            merged_from: head.id.clone(),
            rebased_from: fork.id.clone(),
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_baseline(ctx, rb.clone()).await?;
        let mut fields = BTreeMap::new();
        fields.insert(
            HEADER_BASELINE_ID.to_string(),
            HeaderValue {
                from: Value::String(c.baseline_id.to_string()),
                to: Value::String(head.id.to_string()),
            },
        );
        let e = header_entry(
            &self.new_id(),
            &c.id,
            &self.caller_of(ctx),
            self.now(),
            &HeaderEdit { fields },
        )?;
        tx.append_log(ctx, e).await?;
        if own.rebased_from.is_empty() {
            own.rebased_from = own.fork_baseline.clone();
        }
        own.fork_baseline = head.id.clone();
        own.head = rb.id.clone();
        tx.put_branch(ctx, own).await?;
        let mut c = tx.change(ctx, &c.id).await?;
        c.baseline_id = head.id.clone();
        tx.put_change(ctx, c.clone()).await?;
        // a rebase that met no conflict commits its merges: the branch holds the work brought up to date
        if !res.impacts.is_empty() && !res.changed() {
            self.commit_on_branch(ctx, tx, &c.id, "").await?;
        }
        Ok(res)
    }

    /// committedBehind fills the rebase state of a change that committed on its branch: the head of its target when it
    /// moved since the fork, and the impacts whose node changed on both sides.
    pub(crate) async fn committed_behind(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        own: &Branch,
        st: &mut RebaseState,
    ) -> Result<()> {
        let target = branch_of(&own.parent).to_string();
        let head = branch_head(ctx, tx, &c.namespace, &target).await?;
        if head.id == own.fork_baseline {
            return Ok(());
        }
        st.branch = target;
        st.onto = head.id.clone();
        let fork = tx.baseline(ctx, &own.fork_baseline).await?;
        let mine = branch_head(ctx, tx, &c.namespace, &own.name).await?;
        for cn in main_impacts_of(c) {
            let id = node_of(&cn);
            let Some(hv) = mine.nodes.get(&id) else {
                continue;
            };
            let fv = fork.nodes.get(&id);
            if fv == Some(hv) {
                continue;
            }
            if let Some(tv) = head.nodes.get(&id)
                && Some(tv) != fv
            {
                st.behind.push(cn.id.clone());
            }
        }
        Ok(())
    }

    /// CommitState reads what a change can commit and merge.
    pub async fn commit_state(&self, ctx: &Ctx, id: &ChangeID) -> Result<CommitState> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let mut st = CommitState::default();
        if c.status != goap_domain::CHANGE_DRAFT && c.status != goap_domain::CHANGE_ACTIVE {
            tx.commit(ctx).await?;
            return Ok(st);
        }
        let rows = self.drafts(ctx, &mut tx, id).await?;
        for cn in main_impacts_of(&c) {
            if cn.review != REVIEW_ACCEPTED {
                continue;
            }
            if self
                .draft_modifies(ctx, &mut tx, own_draft(&rows, &cn.id, "").as_ref())
                .await?
            {
                st.to_commit.push(cn.key);
            }
        }
        let (w, _) = self.withdrawn_since(ctx, &mut tx, &c).await?;
        st.unsettled = self.unsettled(ctx, &mut tx, &c).await?;
        st.reverts = w.len();
        st.commits = has_commits(&c);
        st.can_commit = !st.to_commit.is_empty() || st.reverts > 0;
        st.can_merge = st.unsettled.is_empty() && st.commits;
        tx.commit(ctx).await?;
        Ok(st)
    }

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
