//! The merge of a change into the branch it was forked from (Go's `IntegrateChange`, `rebase_committed.go`,
//! `changebranch.go`; ADR 0102, 0140): settled, up to date, then fast-forwarded: what its branch committed joins the
//! target, a baseline of kind fast-forward is the new head, each impact is committed there, the change is applied.
//! A rebase, a three-way merge, a sub-change and a dependency are the Go service's.
use std::collections::BTreeMap;

use serde_json::Value;

use super::{
    commit::NOTHING_TO_COMMIT,
    events::{CHANGE_APPLIED, CHANGE_MERGED, EventNode},
    review::draft_changed,
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::{Change, ChangesFilter},
    impact::{ChangeImpact, ImpactEvent, ImpactOp, LOG_FACT, REVIEW_ACCEPTED, REVIEW_REJECTED},
    model::{Baseline, BaselineId, Branch, ChangeId, MAIN_BRANCH, NodeId, NodeRef, branch_of},
    read,
    store::WriteTx,
};

/// The kind of the baseline a fast-forward leaves (`domain.BaselineFastForward`).
const BASELINE_FAST_FORWARD: &str = "fast-forward";
const BRANCH_MERGED: &str = "merged";
const STATUS_APPLIED: &str = "applied";

fn unserved(what: &str) -> GraphError {
    GraphError::unimplemented(format!("{what}: not served here yet"))
}

/// A change as a merge answers it: its header, its impacts and its items, at the position its log stands at.
#[derive(Debug, Clone, Default)]
pub struct ChangeRead {
    pub change: Change,
    pub impacts: Vec<ChangeImpact>,
    /// the items of its log, as written (`domain.ChangeItem` JSON)
    pub items: Vec<Value>,
    pub seq: i64,
}

/// What a change has to commit and whether it may merge (Go's `CommitState`): what drives the Commit and Merge buttons.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct CommitState {
    /// the keys of the accepted modifications not committed yet
    pub to_commit: Vec<String>,
    /// the nodes withdrawn since their commit, a commit reverts
    pub reverts: usize,
    /// what holds the merge
    pub unsettled: Vec<String>,
    /// an impact committed
    pub commits: bool,
    pub can_commit: bool,
    pub can_merge: bool,
}

impl<S: WriteTx> Tx<'_, S> {
    /// What a change has to commit and whether it may merge; nothing for a change no longer open.
    pub async fn commit_state(&mut self, id: &ChangeId) -> Result<CommitState> {
        let c = self.change_head(id).await?;
        let mut st = CommitState::default();
        if c.status != super::change::STATUS_DRAFT && c.status != super::change::STATUS_ACTIVE {
            return Ok(st);
        }
        self.view(id).await?;
        let impacts: Vec<ChangeImpact> = self.views[id]
            .fold
            .impacts
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded)
            .cloned()
            .collect();
        for cn in &impacts {
            if cn.review == REVIEW_ACCEPTED && self.modifies(id, &cn.id).await? {
                st.to_commit.push(cn.key.clone());
            }
        }
        let own = match self.store.branch(&c.namespace, &c.branch).await? {
            Some(b)
                if branch_of(&c.branch) != MAIN_BRANCH && b.origin == super::change::change_branch_origin(&c.id) =>
            {
                Some(b)
            }
            _ => None,
        };
        if let Some(own) = &own {
            let held: Vec<&ChangeImpact> = impacts.iter().collect();
            st.reverts = super::commit::withdrawn_since(self.store, own, &held).await?.len();
        }
        st.unsettled = self.unsettled(&c, &impacts, own.as_ref()).await?;
        st.commits = impacts.iter().any(|cn| cn.committed.is_some());
        st.can_commit = !st.to_commit.is_empty() || st.reverts > 0;
        st.can_merge = st.unsettled.is_empty() && st.commits;
        Ok(st)
    }

    /// Whether the draft of an impact of the main flow modifies its node (Go's `draftModifies`).
    async fn modifies(&self, id: &ChangeId, impact: &str) -> Result<bool> {
        let Some(d) = self.views[id].fold.draft(impact, "").cloned() else {
            return Ok(false);
        };
        match &d.base {
            None => Ok(true),
            Some(b) => Ok(draft_changed(&self.stored_draft(b).await?, &d)),
        }
    }

    /// What holds the merge of a change (Go's `unsettled`).
    async fn unsettled(&self, c: &Change, impacts: &[ChangeImpact], own: Option<&Branch>) -> Result<Vec<String>> {
        let mut out = Vec::new();
        let points = super::decision::points_of(self.store, &c.id, self.env.now()).await?;
        if let Some(d) = points.iter().find(|d| d.pending()) {
            out.push(format!("pending decision point {:?}", d.question));
        }
        let subs = self
            .store
            .change_heads(&ChangesFilter {
                parent: c.id.clone(),
                ..ChangesFilter::default()
            })
            .await?;
        if !subs.is_empty() {
            return Err(unserved("the merge of a change with sub-changes (ADR 0103)"));
        }
        for cn in impacts {
            if !cn.via.is_empty() {
                continue;
            }
            let modifies = self.modifies(&c.id, &cn.id).await?;
            if cn.review == REVIEW_REJECTED {
                out.push(format!("{} is rejected (rework or withdraw it)", cn.key));
            } else if cn.planned_creation() {
                out.push(format!(
                    "{} is planned, not created yet (create or withdraw it)",
                    cn.key
                ));
            } else if !modifies && cn.committed.is_none() {
                out.push(format!("{} modifies nothing (modify or withdraw it)", cn.key));
            } else if modifies && cn.review != REVIEW_ACCEPTED {
                out.push(format!("{} awaits its review", cn.key));
            } else if modifies {
                out.push(format!("{} is not committed", cn.key));
            }
        }
        if let Some(own) = own {
            let held: Vec<&ChangeImpact> = impacts.iter().collect();
            let w = super::commit::withdrawn_since(self.store, own, &held).await?;
            if !w.is_empty() {
                out.push(format!(
                    "{} withdrawn node(s) still on the branch (commit to revert them)",
                    w.len()
                ));
            }
        }
        Ok(out)
    }

    /// Moves the head of a branch to a baseline built on it (Go's `advanceBranch`): main is created on first use, a
    /// head another transaction moved meanwhile is a conflict.
    async fn advance_branch(&mut self, namespace: &str, name: &str, head: &Baseline) -> Result<()> {
        let ns = crate::model::namespace_of(namespace).to_string();
        self.lock_branch(&ns, name).await?;
        let mut b = match self.store.branch(&ns, name).await? {
            Some(b) => b,
            None if name == MAIN_BRANCH => Branch {
                name: name.into(),
                namespace: ns.clone(),
                status: crate::model::BRANCH_OPEN.into(),
                created_at: Some(self.env.now()),
                ..Branch::default()
            },
            None => return Err(GraphError::not_found(format!("branch {name}"))),
        };
        if b.head != head.parent_id {
            return Err(GraphError::conflict(format!(
                "branch {name} moved to {} while baseline {} was built on {}: merge again",
                b.head, head.id, head.parent_id
            )));
        }
        b.head = head.id.clone();
        self.put_branch(&b).await
    }

    /// Merges a root change into the lasting branch it was forked from, by a fast-forward (Go's `IntegrateChange` of
    /// an open change): the change as it is once applied.
    pub async fn merge_change(&mut self, id: &ChangeId) -> Result<ChangeRead> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if c.status != super::change::STATUS_DRAFT && c.status != super::change::STATUS_ACTIVE {
            if c.status == "committed" && c.parent_id.is_empty() {
                return Err(unserved("the merge of a change waiting for its resolutions"));
            }
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        if !c.parent_id.is_empty() || !c.lifecycle.is_empty() || !c.guardians.is_empty() {
            return Err(unserved(
                "the merge of a sub-change, or of a change following a lifecycle",
            ));
        }
        self.view(id).await?;
        let fold = self.views[id].fold.clone();
        let impacts: Vec<ChangeImpact> = fold
            .impacts
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded)
            .cloned()
            .collect();
        let own = match self.store.branch(&c.namespace, &c.branch).await? {
            Some(b)
                if branch_of(&c.branch) != MAIN_BRANCH && b.origin == super::change::change_branch_origin(&c.id) =>
            {
                Some(b)
            }
            _ => None,
        };
        // settled
        let left = self.unsettled(&c, &impacts, own.as_ref()).await?;
        if !left.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {id} cannot be merged yet: {}",
                left.join("; ")
            )));
        }
        if !impacts.iter().any(|cn| cn.committed.is_some()) {
            return Err(GraphError::invalid(format!(
                "change {id} committed nothing on its branch: {NOTHING_TO_COMMIT}"
            )));
        }
        let Some(mut own) = own else {
            return Err(GraphError::invalid(format!("change {id} has no branch of its own")));
        };
        // up to date: what it is forked from has not moved since (else a rebase, the Go service's)
        let target = branch_of(&own.parent).to_string();
        self.lock_branch(&c.namespace, &target).await?;
        let parent = read::branch(self.store, &own.namespace, &own.parent).await?;
        if parent.ephemeral {
            return Err(unserved("the merge into the branch of a change"));
        }
        let head = read::branch_head(self.store, &own.namespace, &parent.name).await?;
        if head.id != own.fork_baseline {
            return Err(unserved(
                "the merge of a change whose target moved since its fork (a rebase, ADR 0102)",
            ));
        }
        if !c.depends_on.is_empty() {
            return Err(unserved("the merge of a change depending on others (ADR 0108)"));
        }

        // the fast-forward
        let mut ids = self.store.branch_node_ids(&own.name).await?;
        ids.sort();
        let into = read::select(self.store, &head, Some(&ids)).await?;
        let from = read::branch_versions(self.store, &own, &ids).await?;
        let mut moves = Vec::new();
        let mut delta: BTreeMap<NodeId, i32> = BTreeMap::new();
        for n in &ids {
            if let Some(&v) = from.get(n)
                && into.get(n) != Some(&v)
            {
                moves.push(NodeRef::new(n.clone(), v));
                delta.insert(n.clone(), v);
            }
        }
        for n in &ids {
            if !into.contains_key(n) || from.contains_key(n) {
                continue;
            }
            delta.insert(n.clone(), 0);
            if let Some(l) = self.store.latest_on(n, &own.name).await?
                && l.deleted
            {
                moves.push(l.node_ref());
            }
        }
        for r in &moves {
            self.join_branch(r, &target, &c.id).await?;
        }
        own.status = BRANCH_MERGED.into();
        self.put_branch(&own).await?;
        let mut c = c;
        c.status = STATUS_APPLIED.into();
        let res = Baseline {
            id: BaselineId(self.env.new_id()),
            name: c.title.clone(),
            namespace: c.namespace.clone(),
            branch: target.clone(),
            parent_id: head.id.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_FAST_FORWARD.into(),
            created_at: Some(self.env.now()),
            ..Baseline::default()
        };
        let res = self.put_baseline(&res, Some(delta)).await?;
        self.advance_branch(&c.namespace, &target, &res).await?;
        c.result_baseline_id = res.id.clone();

        // each impact committed on the target (Go's `commit`)
        let commits: Vec<&ChangeImpact> = impacts
            .iter()
            .filter(|cn| cn.post.is_some() && (cn.review == REVIEW_ACCEPTED || cn.committed.is_some()))
            .collect();
        let mut committed = Vec::new();
        for cn in commits {
            let mut post = cn.post.clone().unwrap_or_default();
            if post.is_draft()
                && let Some(c) = &cn.committed
            {
                post = c.clone();
            }
            let Some(head) = self.store.latest_on(&post.id, &target).await? else {
                return Err(GraphError::not_found(format!("node {} on {target}", post.id)));
            };
            if head.change_id != c.id && head.node_ref() != post {
                continue;
            }
            let comment = cn.reviews.last().map_or(cn.rationale.clone(), |r| r.comment.clone());
            if head.change_id != c.id || head.change_impact != cn.id || head.comment != comment {
                return Err(unserved("a merge naming a version another change wrote"));
            }
            committed.push(ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Committed,
                committed: Some(head.node_ref()),
                baseline: res.id.clone(),
                branch: target.clone(),
                ..ImpactEvent::default()
            });
        }
        self.emit(committed).await?;
        let moved = if moves.is_empty() {
            Vec::new()
        } else {
            self.store.nodes_at(&moves, "").await?
        };
        let ev: Vec<EventNode> = moved.iter().map(EventNode::of).collect();
        self.raise(CHANGE_MERGED, &c, Some(&res), &target, &ev);
        self.raise(CHANGE_APPLIED, &c, Some(&res), "", &[]);
        self.put_change(&c).await?;

        let mut items = Vec::new();
        for l in self.store.log(id, 0, &[&format!("{LOG_FACT}.")]).await? {
            items.push(serde_json::from_str(&l.payload).map_err(GraphError::internal)?);
        }
        let v = &self.views[id];
        Ok(ChangeRead {
            change: c,
            impacts: v.fold.impacts.clone(),
            items,
            seq: v.seq,
        })
    }
}
