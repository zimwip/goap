//! Rebasing a change (Go's `rebase.go`, `rebase_committed.go`; ADR 0101, 0102, 0103): what it committed on its branch is
//! brought onto what its branch is forked from now (the head of its target, the branch of its parent change), the nodes
//! changed on both sides merged three-way; a clean rebase commits its merges, a conflict sends its impact back to
//! proposed, to be settled.
use std::collections::HashMap;

use serde_json::{Value, json};

use super::{events::CHANGE_REBASED, tx::Tx, work::node_of};
use crate::{
    GraphError, Result,
    change::Change,
    draftmerge::{CONFLICT_NODE, draft_diff, merge_drafts},
    impact::{BaseCopy, ChangeImpact, Draft, ImpactEvent, ImpactOp, REVIEW_ACCEPTED, REVIEW_PROPOSED, Review},
    model::{BaselineId, Branch, ChangeId, LinkId, Node, NodeId, NodeRef, Version, branch_of},
    read,
    store::WriteTx,
};

/// The field of a header edit recording the move of a rebase (`graph.HeaderBaselineID`).
pub const HEADER_BASELINE_ID: &str = "baselineId";
/// Who reviews the merges of a rebase.
const MERGE_REVIEWER: &str = "graph.merge";

/// What a rebase did to an impact (`graphapi.RebasedImpact`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RebasedImpact {
    pub impact: String,
    pub key: String,
    /// its review went back to proposed: the rebase left conflicts on a decided impact
    pub changed: bool,
    /// the node was merged three-way with the target's version
    pub merged: bool,
    pub conflicts: Vec<String>,
}

/// What a rebase did (`graphapi.RebaseResult`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RebaseResult {
    pub parent: ChangeId,
    pub branch: String,
    pub from: BaselineId,
    pub onto: BaselineId,
    pub impacts: Vec<RebasedImpact>,
}

impl RebaseResult {
    /// A rebase that left conflicts: the change has impacts to settle and review again.
    pub fn changed(&self) -> bool {
        self.impacts.iter().any(|i| i.changed || !i.conflicts.is_empty())
    }

    /// What a rebase left to do.
    pub fn summary(&self) -> String {
        let (mut review, mut conflicts) = (Vec::new(), Vec::new());
        for i in &self.impacts {
            if i.changed || !i.conflicts.is_empty() {
                review.push(i.key.clone());
            }
            for c in &i.conflicts {
                conflicts.push(format!("{} {c}", i.key));
            }
        }
        let mut out = format!(
            "rebased onto branch {}: review again {}",
            self.branch,
            review.join(", ")
        );
        if !conflicts.is_empty() {
            out.push_str(&format!("; settle the conflicts {}", conflicts.join(", ")));
        }
        out
    }
}

/// Where a change stands against the branch it was forked from (Go's `RebaseState`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RebaseState {
    pub parent: ChangeId,
    /// the impacts whose node the target changed since the fork too
    pub behind: Vec<String>,
    /// the conflicts a rebase left, per impact: id, key, conflicts
    pub conflicts: Vec<(String, String, Vec<String>)>,
    pub branch: String,
    pub onto: BaselineId,
}

/// What the branch of a change is forked from, as it stands now (Go's `onto`).
pub(crate) struct Onto {
    pub parent: Branch,
    /// the head of a lasting parent; for the branch of a change, the lasting baseline under its chain
    pub head: BaselineId,
}

/// A node a rebase merges three-way (Go's `rebasedNode`).
struct RebasedNode {
    cn: ChangeImpact,
    ours: Draft,
    merged: Draft,
    pre: Option<NodeRef>,
    conflicts: Vec<String>,
}

impl<S: WriteTx> Tx<'_, S> {
    /// What the branch of a change is forked from now (Go's `ontoOf`).
    pub(crate) async fn onto_of(&self, own: &Branch) -> Result<Onto> {
        let p = read::branch(self.store, &own.namespace, &own.parent).await?;
        let head = if p.ephemeral {
            p.fork_baseline.clone()
        } else {
            read::branch_head(self.store, &own.namespace, &p.name).await?.id
        };
        Ok(Onto { parent: p, head })
    }

    /// Whether what the branch is forked from moved since its fork (Go's `onto.moved`).
    pub(crate) async fn moved(&self, to: &Onto, own: &Branch) -> Result<bool> {
        if to.head != own.fork_baseline {
            return Ok(true);
        }
        if !to.parent.ephemeral {
            return Ok(false);
        }
        Ok(self.store.last_commit(&to.parent.name).await? > own.fork_seq)
    }

    /// Some nodes in what the branch is forked from now (Go's `onto.versions`).
    pub(crate) async fn onto_versions(&self, to: &Onto, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
        if to.parent.ephemeral {
            return read::branch_versions(self.store, &to.parent, ids).await;
        }
        read::select(self.store, &read::header_of(self.store, &to.head).await?, Some(ids)).await
    }

    /// Moves the fork of the branch of a change onto what it is forked from now, with the change's reference baseline
    /// (Go's `moveFork`): a change.updated entry records the move.
    pub(crate) async fn move_fork(&mut self, id: &ChangeId, own: &mut Branch, to: &Onto) -> Result<()> {
        let c = self.change_head(id).await?;
        if own.rebased_from.is_empty() {
            own.rebased_from = own.fork_baseline.clone();
        }
        own.fork_baseline = to.head.clone();
        own.fork_seq = 0;
        if to.parent.ephemeral {
            (own.fork_baseline, own.fork_seq) = super::subchange::fork_point(self, &to.parent).await?;
        }
        self.header_entry(
            id,
            json!({HEADER_BASELINE_ID: {"from": c.baseline_id.to_string(), "to": own.fork_baseline.to_string()}}),
        )
        .await?;
        self.put_branch(own).await?;
        let mut c = self.change_head(id).await?;
        c.baseline_id = own.fork_baseline.clone();
        self.put_change(&c).await
    }

    /// The keys of the impacts whose draft holds a modification not committed yet (Go's `uncommitted`).
    async fn uncommitted(&mut self, id: &ChangeId) -> Result<Vec<String>> {
        self.view(id).await?;
        let mut out = Vec::new();
        for cn in main_impacts(&self.views[id].fold.impacts) {
            if self.modifies(id, &cn.id).await? {
                out.push(cn.key.clone());
            }
        }
        Ok(out)
    }

    /// Where a change stands against the branch it was forked from (Go's `RebaseState`): the conflicts left to settle,
    /// and for an open change that committed, the impacts the target moved under.
    pub async fn rebase_state(&mut self, id: &ChangeId) -> Result<RebaseState> {
        let c = self.change_head(id).await?;
        let mut st = RebaseState {
            parent: c.parent_id.clone(),
            ..RebaseState::default()
        };
        self.view(id).await?;
        let fold = &self.views[id].fold;
        for cn in &fold.impacts {
            if let Some(pending) = fold.conflicts.get(&cn.id) {
                st.conflicts.push((cn.id.clone(), cn.key.clone(), pending.clone()));
            }
        }
        if c.status == "applied" || c.status == "abandoned" {
            return Ok(st);
        }
        let impacts = main_impacts(&fold.impacts);
        let Some(own) = self.own_branch(&c).await? else {
            return Ok(st);
        };
        if !impacts.iter().any(|cn| cn.committed.is_some()) {
            return Ok(st);
        }
        let to = self.onto_of(&own).await?;
        if !self.moved(&to, &own).await? {
            return Ok(st);
        }
        st.branch = branch_of(&own.parent).to_string();
        st.onto = to.head.clone();
        let ids: Vec<NodeId> = impacts.iter().filter_map(node_of).collect();
        let fork = read::fork_versions(self.store, &own, &ids).await?;
        let mine = read::branch_versions(self.store, &own, &ids).await?;
        let head = self.onto_versions(&to, &ids).await?;
        for cn in &impacts {
            let Some(n) = node_of(cn) else { continue };
            let Some(&hv) = mine.get(&n) else { continue };
            let fv = fork.get(&n).copied();
            if fv == Some(hv) {
                continue;
            }
            if let Some(&tv) = head.get(&n)
                && Some(tv) != fv
            {
                st.behind.push(cn.id.clone());
            }
        }
        Ok(st)
    }

    /// Rebases what a change committed on its branch onto what that branch is forked from now (Go's `rebaseTx`); a
    /// change that committed nothing has nothing to rebase. merging: for the merge that follows, which moves the fork
    /// itself when nothing merges.
    pub async fn rebase_change(&mut self, id: &ChangeId, merging: bool) -> Result<RebaseResult> {
        let c = self.change_head(id).await?;
        let mut res = RebaseResult {
            parent: c.parent_id.clone(),
            ..RebaseResult::default()
        };
        if c.status == "applied" || c.status == "abandoned" {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        let Some(own) = self.own_branch(&c).await? else {
            return Ok(res);
        };
        self.view(id).await?;
        let open = c.status == super::change::STATUS_DRAFT || c.status == super::change::STATUS_ACTIVE;
        if !open
            || !main_impacts(&self.views[id].fold.impacts)
                .iter()
                .any(|cn| cn.committed.is_some())
        {
            return Ok(res);
        }
        let parent = res.parent.clone();
        res = self.rebase_onto(&c, own, merging).await?;
        res.parent = parent;
        Ok(res)
    }

    /// Go's `rebaseCommittedTx`: the fork moved there, each node changed on both sides since the fork merged three-way
    /// (base: the version at the fork; ours: the version the change committed; theirs: the version on the target).
    async fn rebase_onto(&mut self, c: &Change, mut own: Branch, merging: bool) -> Result<RebaseResult> {
        let target = branch_of(&own.parent).to_string();
        let mut res = RebaseResult {
            branch: target.clone(),
            from: own.fork_baseline.clone(),
            ..RebaseResult::default()
        };
        let to = self.onto_of(&own).await?;
        res.onto = to.head.clone();
        if !self.moved(&to, &own).await? {
            return Ok(res);
        }
        let keys = self.uncommitted(&c.id).await?;
        if !keys.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {} holds uncommitted modifications ({}): commit or cancel them before it is rebased",
                c.id,
                keys.join(", ")
            )));
        }
        let impacts = main_impacts(&self.views[&c.id].fold.impacts);
        let mut impact_of: HashMap<NodeId, ChangeImpact> = HashMap::new();
        for cn in &impacts {
            if let Some(n) = node_of(cn) {
                impact_of.insert(n, cn.clone());
            }
        }
        // the nodes the branch holds a version of and the ones the impacts start from, never the whole states
        let mut ids = self.store.branch_node_ids(&own.name).await?;
        ids.extend(impacts.iter().filter_map(|cn| cn.pre.as_ref().map(|p| p.id.clone())));
        ids.sort();
        ids.dedup();
        let fork = read::fork_versions(self.store, &own, &ids).await?;
        let mine = read::branch_versions(self.store, &own, &ids).await?;
        let head = self.onto_versions(&to, &ids).await?;
        // the link sets of the versions merged on both sides: a side whose set is the one the sides started from left
        // its links alone
        let mut set_refs = Vec::new();
        for id in &ids {
            if let (Some(&hv), Some(&fv), Some(&tv)) = (mine.get(id), fork.get(id), head.get(id))
                && fv != hv
                && tv != fv
            {
                for v in [fv, hv, tv] {
                    set_refs.push(NodeRef::new(id.clone(), v));
                }
            }
        }
        let sets = self.store.link_sets_of(&set_refs).await?;
        let mut merges = Vec::new();
        for id in &ids {
            let Some(&hv) = mine.get(id) else { continue };
            let fv = fork.get(id).copied();
            if fv == Some(hv) {
                continue; // the change did not change it: the target's version stands
            }
            let tv = head.get(id).copied();
            if tv == fv {
                continue; // changed by the change only
            }
            let Some(cn) = impact_of.get(id).cloned() else {
                return Err(GraphError::conflict(format!(
                    "node {id} changed on the branch of change {} by no impact of it",
                    c.id
                )));
            };
            let ours_ref = NodeRef::new(id.clone(), hv);
            let ours = self.stored_draft(&ours_ref).await?;
            let (mut merged, conflicts) = match (fv, tv) {
                (Some(fv), Some(tv)) => {
                    let base_ref = NodeRef::new(id.clone(), fv);
                    let theirs_ref = NodeRef::new(id.clone(), tv);
                    let of = |r: &NodeRef| sets.get(r).map(|s| s.of);
                    let known = of(&base_ref).is_some() && of(&ours_ref).is_some() && of(&theirs_ref).is_some();
                    let ours_links = !known || of(&ours_ref) != of(&base_ref);
                    let theirs_links = !known || of(&theirs_ref) != of(&base_ref);
                    let mut base = self.stored_version(&base_ref, ours_links && theirs_links).await?;
                    let mut theirs = self.stored_version(&theirs_ref, theirs_links).await?;
                    if !theirs_links {
                        // the target left the links alone: the change's stand
                        base.links = ours.links.clone();
                        theirs.links = ours.links.clone();
                    } else if !ours_links {
                        // the change left them alone: the target's stand
                        base.links = ours.links.clone();
                    }
                    let (mut m, cs) = merge_drafts(&base, &ours, &theirs);
                    for l in m.links.iter_mut() {
                        if l.id.0.is_empty() {
                            l.id = LinkId(self.env.new_id());
                        }
                    }
                    (m, cs)
                }
                _ => (ours.clone(), vec![CONFLICT_NODE.to_string()]),
            };
            if conflicts.is_empty() && !super::review::draft_changed(&ours, &merged) {
                continue; // what the target changed, the change holds already
            }
            let theirs_ref = NodeRef::new(id.clone(), tv.unwrap_or_default());
            merged.change = c.id.clone();
            merged.impact = cn.id.clone();
            merged.flow = String::new();
            merged.base = Some(ours_ref.clone());
            merged.merged_with = Some(theirs_ref.clone());
            let pre = (cn.pre.is_some() && tv.is_some()).then(|| theirs_ref.clone());
            res.impacts.push(RebasedImpact {
                impact: cn.id.clone(),
                key: cn.key.clone(),
                changed: !conflicts.is_empty() && cn.review != REVIEW_PROPOSED,
                merged: true,
                conflicts: conflicts.clone(),
            });
            merges.push(RebasedNode {
                cn,
                ours,
                merged,
                pre,
                conflicts,
            });
        }
        // the impacts that committed nothing start from the target's version of their node too
        let mut repre = Vec::new();
        for cn in &impacts {
            let Some(p) = &cn.pre else { continue };
            if cn.committed.is_some() {
                continue;
            }
            if let Some(&v) = head.get(&p.id)
                && v != p.version
            {
                repre.push(ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: ImpactOp::Rebased,
                    pre: Some(NodeRef::new(p.id.clone(), v)),
                    ..ImpactEvent::default()
                });
            }
        }
        if merging && merges.is_empty() && repre.is_empty() {
            // no node changed on both sides: the merge commits what the branch committed over the target as it is,
            // the fork moved by the fast-forward
            return Ok(res);
        }
        // the rebase is one entry naming what the branch is brought onto, then the nodes it changes, then the review
        // of the merge, then its commit
        self.move_fork(&c.id, &mut own, &to).await?;
        let rebased = json!({"branch": target, "baseline": to.head.to_string()});
        self.install_rebased(&c.id, merges, rebased, &format!("rebased onto {target}"))
            .await?;
        self.emit(repre).await?;
        let mut wrote = Vec::new();
        if !res.impacts.is_empty() && !res.changed() {
            wrote = self.commit_on_branch(&c.id, "", true).await?.1;
        }
        self.raise_rebased(&c.id, &own.name, &wrote).await?;
        Ok(res)
    }

    /// Gives each merged node a draft (Go's `installRebased`): checked out from the version the change committed and
    /// updated with the difference the merge makes; the clean ones accepted by a review of graph.merge, the conflicting
    /// ones sent back to proposed.
    async fn install_rebased(
        &mut self,
        id: &ChangeId,
        nodes: Vec<RebasedNode>,
        rebased: Value,
        why: &str,
    ) -> Result<()> {
        let mut entries = Vec::new();
        let (mut accepted, mut reopened) = (Vec::new(), Vec::new());
        for n in nodes {
            let event = self.env.new_id();
            let base = n.ours.base.clone().unwrap_or_default();
            let draft_ref = NodeRef::new(base.id.clone(), 0);
            let mut copied = n.ours.clone();
            copied.change = id.clone();
            copied.impact = n.cn.id.clone();
            copied.flow = String::new();
            copied.merged_with = n.merged.merged_with.clone();
            for l in copied.links.iter_mut() {
                l.id = crate::impact::base_link_id(&event, &l.id);
            }
            let mut merged = n.merged.clone();
            for l in merged.links.iter_mut() {
                l.id = LinkId(self.env.new_id()); // a link the merge adds is its own
            }
            let mut patch = draft_diff(&copied, &merged).unwrap_or_default();
            patch.insert("rebased".into(), rebased.clone());
            patch.insert(
                "conflicts".into(),
                if n.conflicts.is_empty() {
                    Value::Null
                } else {
                    json!(n.conflicts)
                },
            );
            let checkout = ImpactEvent {
                id: event,
                change: id.clone(),
                impact: n.cn.id.clone(),
                op: ImpactOp::CheckedOut,
                execution: n.ours.execution.clone(),
                post: Some(draft_ref.clone()),
                pre: n.pre.clone(),
                from_base: Some(BaseCopy::default()),
                draft: Some(copied),
                ..ImpactEvent::default()
            };
            let update = ImpactEvent {
                change: id.clone(),
                impact: n.cn.id.clone(),
                op: ImpactOp::Updated,
                execution: n.ours.execution.clone(),
                post: Some(draft_ref),
                patch: Some(patch),
                ..ImpactEvent::default()
            };
            self.emit(vec![checkout, update]).await?;
            if n.conflicts.is_empty() {
                entries.push((n.cn.id.clone(), why.to_string()));
                accepted.push(n.cn.id.clone());
            } else if n.cn.review != REVIEW_PROPOSED {
                let r = Review {
                    status: REVIEW_PROPOSED.into(),
                    by: self.env.by.clone(),
                    at: self.env.now(),
                    comment: why.to_string(),
                    ..Review::default()
                };
                reopened.push(ImpactEvent {
                    change: id.clone(),
                    impact: n.cn.id.clone(),
                    op: ImpactOp::Reviewed,
                    by: r.by.clone(),
                    review: Some(r),
                    ..ImpactEvent::default()
                });
            }
        }
        if !entries.is_empty() {
            let (key, cause) = self.put_review_record(id, MERGE_REVIEWER, why, &entries).await?;
            let mut events = Vec::with_capacity(accepted.len());
            for impact in accepted {
                events.push(ImpactEvent {
                    change: id.clone(),
                    impact,
                    op: ImpactOp::Reviewed,
                    by: MERGE_REVIEWER.into(),
                    review: Some(Review {
                        status: REVIEW_ACCEPTED.into(),
                        by: MERGE_REVIEWER.into(),
                        comment: why.to_string(),
                        at: self.env.now(),
                        review_id: key.clone(),
                        ..Review::default()
                    }),
                    causation: cause.clone(),
                    ..ImpactEvent::default()
                });
            }
            self.emit(events).await?;
        }
        self.emit(reopened).await
    }

    /// A stored version seen as a draft, its links read only when asked (Go's `storedVersion`).
    async fn stored_version(&self, r: &NodeRef, with_links: bool) -> Result<Draft> {
        if with_links {
            return self.stored_draft(r).await;
        }
        let n = read::stored_node(self.store, r).await?;
        Ok(Draft {
            node: n.id.clone(),
            key: n.key.clone(),
            typ: n.typ.clone(),
            state: n.state.clone(),
            owner: n.owner.clone(),
            props: n.props.clone(),
            base: Some(r.clone()),
            ..Draft::default()
        })
    }

    /// Raises change.rebased: the branch the change's work is on, the versions its rebase wrote.
    pub(crate) async fn raise_rebased(&mut self, id: &ChangeId, branch: &str, wrote: &[Node]) -> Result<()> {
        let c = self.change_head(id).await?;
        let ev: Vec<_> = wrote.iter().map(super::events::EventNode::of).collect();
        self.raise(CHANGE_REBASED, &c, None, branch_of(branch), &ev);
        Ok(())
    }
}

/// The impacts of the main flow of a change (Go's `mainImpactsOf`).
pub(crate) fn main_impacts(impacts: &[ChangeImpact]) -> Vec<ChangeImpact> {
    impacts
        .iter()
        .filter(|cn| cn.flow.is_empty() && !cn.superseded)
        .cloned()
        .collect()
}
