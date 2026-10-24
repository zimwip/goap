//! The merge of a change into the branch it was forked from (Go's `IntegrateChange`, `rebase_committed.go`,
//! `changebranch.go`; ADR 0102, 0140): settled, up to date, then fast-forwarded: what its branch committed joins the
//! target, a baseline of kind fast-forward is the new head, each impact is committed there, the change is applied.
//! A sub-change merges into the branch of its parent the same way (ADR 0103). A three-way merge and a dependency are
//! the Go service's.
use std::collections::{BTreeMap, HashMap, HashSet};

use serde_json::Value;

use super::{
    commit::NOTHING_TO_COMMIT,
    events::{CHANGE_APPLIED, CHANGE_MERGED, EventNode},
    rebase::main_impacts,
    review::draft_changed,
    tx::Tx,
    work::node_of,
};
use crate::{
    GraphError, Result,
    change::{Change, ChangesFilter},
    impact::{
        ChangeImpact, ImpactEvent, ImpactOp, LOG_FACT, REVIEW_ACCEPTED, REVIEW_PROPOSED, REVIEW_REJECTED, Review,
    },
    model::{BRANCH_MERGED, Baseline, BaselineId, Branch, ChangeId, MAIN_BRANCH, NodeId, NodeRef, Version, branch_of},
    read,
    store::WriteTx,
};

/// The kind of the baseline a fast-forward leaves (`domain.BaselineFastForward`).
const BASELINE_FAST_FORWARD: &str = "fast-forward";
const STATUS_APPLIED: &str = "applied";
/// Who records the work of a merged sub-change in its parent.
const MERGE_PRODUCER: &str = "graph.merge";

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

/// What a merge did: the change applied, or a rebase kept that left impacts to look at again (the error the merge
/// answers, once the rebase is committed).
#[derive(Debug)]
pub enum Merge {
    Applied(Box<ChangeRead>),
    Rebased(GraphError),
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
    pub(super) async fn modifies(&self, id: &ChangeId, impact: &str) -> Result<bool> {
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
        if let Some(f) = super::flow::flows_of(self.store, &c.id)
            .await?
            .list
            .iter()
            .find(|f| f.is_open())
        {
            out.push(format!("open flow {} (adopt or discard it)", f.id));
        }
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
        let open = subs
            .iter()
            .filter(|s| s.status != STATUS_APPLIED && s.status != super::change::STATUS_ABANDONED)
            .count();
        if open > 0 {
            out.push(format!("{open} open sub-change(s) (merge or abandon them)"));
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
    pub(super) async fn advance_branch(&mut self, namespace: &str, name: &str, head: &Baseline) -> Result<()> {
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

    /// Merges a change into what it was forked from (Go's `IntegrateChange` of an open change): settled, rebased onto
    /// what its branch is forked from now, then fast-forwarded: a root change into the lasting branch it was forked from,
    /// a sub-change into the branch of its parent, which takes its work as its own (ADR 0103). A rebase that left
    /// impacts to look at again is kept, the merge refused: [`Merge::Rebased`], whose transaction is committed.
    pub async fn merge_change(&mut self, id: &ChangeId) -> Result<Merge> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if c.status != super::change::STATUS_DRAFT && c.status != super::change::STATUS_ACTIVE {
            if c.status == "committed" && c.parent_id.is_empty() {
                return Err(unserved("the merge of a change waiting for its resolutions"));
            }
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        if !c.lifecycle.is_empty() || !c.guardians.is_empty() {
            return Err(unserved("the merge of a change following a lifecycle"));
        }
        self.view(id).await?;
        let impacts = main_impacts(&self.views[id].fold.impacts);
        let own = self.own_branch(&c).await?;
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
        self.check_parent_drafts(&c, &impacts).await?;
        if !c.depends_on.is_empty() {
            return Err(unserved("the merge of a change depending on others (ADR 0108)"));
        }
        // the target is locked before the rebase reads its head: no other merge commits between the rebase and the
        // fast-forward
        if let Some(own) = &own {
            self.lock_branch(&c.namespace, branch_of(&own.parent)).await?;
        }
        let rebased = self.rebase_change(id, true).await?;
        if rebased.changed() {
            return Ok(Merge::Rebased(GraphError::conflict(format!(
                "change {id} {}",
                rebased.summary()
            ))));
        }
        let mut c = self.change_head(id).await?;
        let Some(mut own) = self.own_branch(&c).await? else {
            return Err(GraphError::invalid(format!("change {id} has no branch of its own")));
        };
        // the fast-forward (Go's `fastForward`): what the branch changed since its fork joins the target, which must
        // not have moved on those nodes
        let to = self.onto_of(&own).await?;
        let mut ids = self.store.branch_node_ids(&own.name).await?;
        ids.sort();
        let fork = read::fork_versions(self.store, &own, &ids).await?;
        let into = self.onto_versions(&to, &ids).await?;
        if fork != into {
            if to.parent.ephemeral {
                return Err(GraphError::conflict(format!(
                    "the branch of parent change {} moved meanwhile: merge {} again",
                    c.parent_id, c.id
                )));
            }
            return Err(unserved("the three-way merge of a change whose target moved meanwhile"));
        }
        if self.moved(&to, &own).await? {
            // moved on other nodes: the work of the branch stands over it as it is, the fork moved
            self.move_fork(id, &mut own, &to).await?;
            self.raise_rebased(id, &own.name, &[]).await?;
            c = self.change_head(id).await?;
        }
        self.view(id).await?;
        let impacts = main_impacts(&self.views[id].fold.impacts);
        let from = read::branch_versions(self.store, &own, &ids).await?;
        let (moves, delta) = self.moves(&own, &ids, &into, &from).await?;
        let target = to.parent.name.clone();
        for r in &moves {
            self.join_branch(r, &target, &c.id).await?;
        }
        own.status = BRANCH_MERGED.into();
        self.put_branch(&own).await?;
        c.status = STATUS_APPLIED.into();
        if to.parent.ephemeral {
            // the branch of the parent holds what the sub-change committed, its impacts committed there
            self.end_commit(&target);
            self.commit_impacts(&c, &impacts, &target, &BaselineId::default())
                .await?;
            let ev = self.moved_nodes(&moves).await?;
            self.raise(CHANGE_MERGED, &c, None, &target, &ev);
            self.put_change(&c).await?;
            self.record_in_parent(&c).await?;
            return self.change_read(c).await.map(|r| Merge::Applied(Box::new(r)));
        }
        let res = Baseline {
            id: BaselineId(self.env.new_id()),
            name: c.title.clone(),
            namespace: c.namespace.clone(),
            branch: target.clone(),
            parent_id: to.head.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_FAST_FORWARD.into(),
            created_at: Some(self.env.now()),
            ..Baseline::default()
        };
        let res = self.put_baseline(&res, Some(delta)).await?;
        self.advance_branch(&c.namespace, &target, &res).await?;
        c.result_baseline_id = res.id.clone();
        self.commit_impacts(&c, &impacts, &target, &res.id).await?;
        let ev = self.moved_nodes(&moves).await?;
        self.raise(CHANGE_MERGED, &c, Some(&res), &target, &ev);
        self.raise(CHANGE_APPLIED, &c, Some(&res), "", &[]);
        self.put_change(&c).await?;
        self.change_read(c).await.map(|r| Merge::Applied(Box::new(r)))
    }

    /// What a fast-forward moves (Go's `fastForward`): the versions the branch holds that the target does not, the
    /// tombstones of the nodes it retired, and the delta of the state of the target.
    async fn moves(
        &self,
        own: &Branch,
        ids: &[NodeId],
        into: &HashMap<NodeId, Version>,
        from: &HashMap<NodeId, Version>,
    ) -> Result<(Vec<NodeRef>, BTreeMap<NodeId, i32>)> {
        let mut moves = Vec::new();
        let mut delta: BTreeMap<NodeId, i32> = BTreeMap::new();
        for n in ids {
            if let Some(&v) = from.get(n)
                && into.get(n) != Some(&v)
            {
                moves.push(NodeRef::new(n.clone(), v));
                delta.insert(n.clone(), v);
            }
        }
        for n in ids {
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
        Ok((moves, delta))
    }

    async fn moved_nodes(&self, moves: &[NodeRef]) -> Result<Vec<EventNode>> {
        if moves.is_empty() {
            return Ok(Vec::new());
        }
        Ok(self
            .store
            .nodes_at(moves, "")
            .await?
            .iter()
            .map(EventNode::of)
            .collect())
    }

    /// Commits each impact of a merged change on its target (Go's `commit`): the version of the target it wrote, its
    /// origin made the change's.
    async fn commit_impacts(
        &mut self,
        c: &Change,
        impacts: &[ChangeImpact],
        target: &str,
        result: &BaselineId,
    ) -> Result<()> {
        let mut committed = Vec::new();
        for cn in impacts {
            if cn.post.is_none() || (cn.review != REVIEW_ACCEPTED && cn.committed.is_none()) {
                continue;
            }
            let mut post = cn.post.clone().unwrap_or_default();
            if post.is_draft()
                && let Some(c) = &cn.committed
            {
                post = c.clone();
            }
            let Some(head) = self.store.latest_on(&post.id, target).await? else {
                return Err(GraphError::not_found(format!("node {} on {target}", post.id)));
            };
            if head.change_id != c.id && head.node_ref() != post {
                continue;
            }
            let r = head.node_ref();
            let comment = cn.reviews.last().map_or(cn.rationale.clone(), |r| r.comment.clone());
            if head.change_id != c.id || head.change_impact != cn.id || head.comment != comment {
                self.set_node_origin(&r, &c.id, &cn.id, &comment).await?;
            }
            committed.push(ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Committed,
                committed: Some(r),
                baseline: result.clone(),
                branch: target.to_string(),
                ..ImpactEvent::default()
            });
        }
        self.emit(committed).await
    }

    /// Refuses the merge of a sub-change while its parent holds an uncommitted modification of a node the sub-change
    /// changed (Go's `checkParentDrafts`).
    async fn check_parent_drafts(&mut self, c: &Change, impacts: &[ChangeImpact]) -> Result<()> {
        if c.parent_id.is_empty() {
            return Ok(());
        }
        let parent = c.parent_id.clone();
        self.view(&parent).await?;
        let mine: HashSet<NodeId> = impacts.iter().filter_map(node_of).collect();
        let mut held = Vec::new();
        for cn in main_impacts(&self.views[&parent].fold.impacts) {
            if node_of(&cn).is_some_and(|n| mine.contains(&n)) && self.modifies(&parent, &cn.id).await? {
                held.push(cn.key.clone());
            }
        }
        if held.is_empty() {
            return Ok(());
        }
        Err(GraphError::conflict(format!(
            "parent change {parent} holds uncommitted modifications of {}: it commits or cancels them first",
            held.join(", ")
        )))
    }

    /// Gives the parent of a merged sub-change an impact for each node the sub-change committed (Go's
    /// `recordInParent`): its own impact of the node, else a new one, accepted by one review of `graph.merge` and
    /// committed as the version the sub-change committed.
    async fn record_in_parent(&mut self, c: &Change) -> Result<()> {
        let parent = self.change_head(&c.parent_id).await?;
        self.view(&parent.id).await?;
        let mut held: HashMap<NodeId, String> = HashMap::new();
        for p in main_impacts(&self.views[&parent.id].fold.impacts) {
            if let Some(n) = node_of(&p) {
                held.insert(n, p.id.clone());
            }
        }
        let mut todo = Vec::new();
        let mut entries = Vec::new();
        for cn in main_impacts(&self.views[&c.id].fold.impacts) {
            let Some(r) = cn.committed.clone() else { continue };
            let mut events = Vec::new();
            let id = match held.get(&r.id) {
                Some(id) => id.clone(),
                None => {
                    let id = self.env.new_id();
                    let st = ChangeImpact {
                        id: id.clone(),
                        key: cn.key.clone(),
                        typ: cn.typ.clone(),
                        intent: cn.intent.clone(),
                        rationale: cn.rationale.clone(),
                        pre: cn.pre.clone(),
                        review: REVIEW_PROPOSED.into(),
                        produced_by: MERGE_PRODUCER.into(),
                        derived_from: vec![cn.id.clone()],
                        execution: cn.execution.clone(),
                        created_at: self.env.now(),
                        ..ChangeImpact::default()
                    };
                    let (op, post) = if cn.pre.is_some() {
                        (ImpactOp::Proposed, None)
                    } else {
                        (ImpactOp::Created, Some(r.clone()))
                    };
                    events.push(ImpactEvent {
                        change: parent.id.clone(),
                        impact: id.clone(),
                        op,
                        state: Some(st),
                        post,
                        ..ImpactEvent::default()
                    });
                    id
                }
            };
            let last = cn
                .reviews
                .iter()
                .rfind(|r| r.flow.is_empty() && !r.superseded)
                .cloned()
                .unwrap_or_default();
            let review = Review {
                status: REVIEW_ACCEPTED.into(),
                by: last.by.clone(),
                at: self.env.now(),
                comment: format!("merged from sub-change {} ({}): {}", c.title, c.id, last.comment)
                    .trim()
                    .to_string(),
                ..Review::default()
            };
            entries.push((id.clone(), review.comment.clone()));
            todo.push((events, id, r, review));
        }
        if todo.is_empty() {
            return Ok(());
        }
        let (key, cause) = self
            .put_review_record(
                &parent.id,
                MERGE_PRODUCER,
                &format!("merge of sub-change {} ({})", c.title, c.id),
                &entries,
            )
            .await?;
        // by phase, so that the verdicts of the review and the versions of the commit are one entry each (ADR 0144)
        let mut phases: [Vec<ImpactEvent>; 3] = Default::default();
        let branch = branch_of(&parent.branch).to_string();
        for (events, id, r, mut review) in todo {
            review.review_id = key.clone();
            phases[0].extend(events);
            phases[1].push(ImpactEvent {
                change: parent.id.clone(),
                impact: id.clone(),
                op: ImpactOp::Reviewed,
                by: review.by.clone(),
                review: Some(review),
                causation: cause.clone(),
                ..ImpactEvent::default()
            });
            phases[2].push(ImpactEvent {
                change: parent.id.clone(),
                impact: id,
                op: ImpactOp::Committed,
                committed: Some(r),
                branch: branch.clone(),
                ..ImpactEvent::default()
            });
        }
        for events in phases {
            self.emit(events).await?;
        }
        Ok(())
    }

    /// A change as a merge answers it.
    async fn change_read(&mut self, c: Change) -> Result<ChangeRead> {
        let id = c.id.clone();
        let mut items = Vec::new();
        for l in self.store.log(&id, 0, &[&format!("{LOG_FACT}.")]).await? {
            items.push(serde_json::from_str(&l.payload).map_err(GraphError::internal)?);
        }
        let v = &self.views[&id];
        Ok(ChangeRead {
            change: c,
            impacts: v.fold.impacts.clone(),
            items,
            seq: v.seq,
        })
    }
}
