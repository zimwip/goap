//! Rebasing the drafts a commit writes (Go's `rebase_drafts.go`, ADR 0165): before a commit writes its versions, each
//! accepted modification it writes is compared with the head of the branch the change merges into. A node the head
//! moved since its draft was checked out is merged three-way (base: the common ancestor of the two versions; ours: the
//! draft; theirs: the head's version), its draft checked out again from the head's version and updated with the
//! difference the merge makes; its state moves only by moves its lifecycle allows from the head's state (the moves the
//! change recorded replayed, or one move when both sides moved it), else a conflict of states. A clean rebase is
//! reviewed by graph.merge and the commit goes on; a conflict stops the commit, the rebase kept.
use std::collections::HashSet;

use graph_catalog::def::Lifecycle;
use serde_json::{Value, json};

use super::{rebase::HEADER_BASELINE_ID, rebase::main_impacts, tx::Tx};
use crate::{
    GraphError, Result,
    draftmerge::{CONFLICT_STATE, draft_diff, merge_drafts},
    impact::{BaseCopy, BaseLink, BaseNode, ChangeImpact, Draft, ImpactEvent, ImpactOp, REVIEW_ACCEPTED, copy_draft},
    model::{ChangeId, LinkId, NodeId, NodeRef, Target, Version, branch_of},
    read,
    store::WriteTx,
};

/// A draft the head moved under (Go's `draftRebase`).
struct DraftRebase {
    cn: ChangeImpact,
    draft: Draft,
    head: NodeRef,
    ancestor: NodeRef,
}

/// A move of a lifecycle a rebase replays.
#[derive(Debug, Clone, PartialEq)]
struct StateMove {
    from: String,
    to: String,
}

impl<S: WriteTx> Tx<'_, S> {
    /// Rebases the drafts a commit of the nodes named (none: every one) writes onto the head of the branch the change
    /// merges into (Go's `rebaseDraftsTx`): the branch, and the impacts left with conflicts (key and conflicts).
    pub(crate) async fn rebase_drafts(&mut self, id: &ChangeId, only: &[NodeId]) -> Result<(String, Vec<String>)> {
        let c = self.change_head(id).await?;
        if [
            super::change::STATUS_APPLIED,
            super::change::STATUS_ABANDONED,
            super::change::STATUS_COMMITTED,
        ]
        .contains(&c.status.as_str())
        {
            return Ok((String::new(), Vec::new())); // the commit refuses it
        }
        let own = self.own_branch(&c).await?;
        self.view(id).await?;
        let impacts = main_impacts(&self.views[id].fold.impacts);
        let refs: Vec<&ChangeImpact> = impacts.iter().collect();
        // the nodes the change holds a draft of: a link to one is a link to its draft
        let mut drafted: HashSet<NodeId> = HashSet::new();
        let mut todo = Vec::new();
        let mut ids = Vec::new();
        for cn in &impacts {
            let Some(d) = self.views[id].fold.draft(&cn.id, "") else {
                continue;
            };
            drafted.insert(d.node.clone());
            if d.base.is_none() || cn.review != REVIEW_ACCEPTED || !super::commit::takes_impact(only, &refs, cn) {
                continue;
            }
            ids.push(d.node.clone());
            todo.push((cn.clone(), d.clone()));
        }
        if todo.is_empty() {
            return Ok((String::new(), Vec::new()));
        }
        // the head of the branch the change merges into, read for those nodes only; a change that committed nothing
        // starts from it now
        let follow = c.status == super::change::STATUS_DRAFT || c.status == super::change::STATUS_ACTIVE;
        let follow = follow && !impacts.iter().any(|cn| cn.committed.is_some());
        let (target, head) = match own.clone() {
            Some(mut own) => {
                let to = self.onto_of(&own).await?;
                let head = self.onto_versions(&to, &ids).await?;
                if follow && self.moved(&to, &own).await? {
                    self.move_fork(id, &mut own, &to).await?;
                    self.raise_rebased(id, &own.name, &[]).await?;
                }
                (to.parent.name.clone(), head)
            }
            None => {
                let target = branch_of(&c.branch).to_string();
                let h = read::branch_head(self.store, &c.namespace, &target).await?;
                let head = read::select(self.store, &h, Some(&ids)).await?;
                if follow && h.id != c.baseline_id {
                    self.move_baseline(id, &h.id).await?;
                }
                (target, head)
            }
        };
        let mut moved = Vec::new();
        for (cn, d) in todo {
            let base = d.base.clone().expect("filtered");
            let Some(&tv) = head.get(&base.id) else { continue };
            if tv == base.version {
                continue;
            }
            if let Some(own) = &own {
                // a version the change committed, checked out again: the rebase of what it committed brings it up
                let n = read::stored_node(self.store, &base).await?;
                if branch_of(&n.branch) == own.name {
                    continue;
                }
            }
            let Some(anc) = super::branchmerge::common_ancestor(self.store, &base.id, base.version, tv).await? else {
                return Err(GraphError::not_found(format!(
                    "versions {} and {tv} of node {} have no common ancestor",
                    base.version, base.id
                )));
            };
            if anc.version == tv {
                continue; // the draft starts from a version over the head's already
            }
            moved.push(DraftRebase {
                cn,
                draft: d,
                head: NodeRef::new(base.id.clone(), tv),
                ancestor: anc,
            });
        }
        if moved.is_empty() {
            return Ok((target, Vec::new()));
        }
        let rebased = json!({"branch": target, "drafts": true});
        let why = format!("rebased onto {target}");
        let mut done = Vec::with_capacity(moved.len());
        let mut left = Vec::new();
        for r in moved {
            let d = &r.draft;
            let theirs = read::stored_node(self.store, &r.head).await?;
            let theirs_links = self.store.links_from(std::slice::from_ref(&r.head), None).await?;
            // the draft checked out again from the head's version: its copy, the links to a node the change drafts
            // turned into links to its draft
            let event = self.env.new_id();
            let mut cp = BaseCopy::default();
            let mut links = Vec::with_capacity(theirs_links.len());
            for l in theirs_links {
                let (to, target) = match l.to {
                    Target::Node(t) => (t, Default::default()),
                    Target::External(u) => (NodeRef::default(), u),
                };
                if !to.id.as_str().is_empty() && drafted.contains(&to.id) && !cp.drafted.contains(&to) {
                    cp.drafted.push(to.clone());
                }
                links.push(BaseLink {
                    id: l.id,
                    typ: l.typ,
                    to,
                    target,
                    props: l.props,
                });
            }
            let mut checkout = ImpactEvent {
                id: event,
                change: c.id.clone(),
                impact: r.cn.id.clone(),
                op: ImpactOp::CheckedOut,
                execution: d.execution.clone(),
                from_base: Some(cp),
                draft: Some(Draft {
                    execution: d.execution.clone(),
                    ..Draft::default()
                }),
                ..ImpactEvent::default()
            };
            let base_node = BaseNode {
                node: r.head.clone(),
                key: theirs.key.clone(),
                typ: theirs.typ.clone(),
                state: theirs.state.clone(),
                owner: theirs.owner.clone(),
                props: theirs.props.clone(),
            };
            let copied = copy_draft(&checkout, &base_node, &links);
            let mut anc = self.stored_draft(&r.ancestor).await?;
            for l in anc.links.iter_mut() {
                if !l.is_external() && drafted.contains(&l.to.id) {
                    l.to = NodeRef::new(l.to.id.clone(), 0);
                }
            }
            let (mut merged, mut conflicts) = merge_drafts(&anc, d, &copied);
            for l in merged.links.iter_mut() {
                if l.id.0.is_empty() {
                    l.id = LinkId(self.env.new_id());
                }
            }
            let mut moves = Vec::new();
            if let Some(lc) = self.catalog().lifecycle(&d.typ) {
                conflicts.retain(|s| s != CONFLICT_STATE);
                match rebased_moves(
                    lc,
                    &self.views[id].events,
                    &r.cn.id,
                    &anc.state,
                    &d.state,
                    &copied.state,
                ) {
                    Some(m) => moves = m,
                    None => {
                        conflicts.push(CONFLICT_STATE.to_string());
                        conflicts.sort();
                    }
                }
                merged.state = copied.state.clone(); // the state moves by the moves replayed, never by the update
            }
            merged.change = c.id.clone();
            merged.impact = r.cn.id.clone();
            merged.flow = String::new();
            merged.base = Some(r.head.clone());
            merged.merged_with = None;
            let draft_ref = NodeRef::new(r.head.id.clone(), 0);
            checkout.post = Some(draft_ref.clone());
            checkout.pre = r.cn.pre.as_ref().map(|_| r.head.clone()); // the impact now starts from the head's version
            let mut patch = draft_diff(&copied, &merged).unwrap_or_default();
            patch.insert("rebased".into(), rebased.clone());
            patch.insert(
                "conflicts".into(),
                if conflicts.is_empty() {
                    Value::Null
                } else {
                    json!(conflicts)
                },
            );
            checkout.draft = Some(copied);
            let mut evs = vec![
                checkout,
                ImpactEvent {
                    change: c.id.clone(),
                    impact: r.cn.id.clone(),
                    op: ImpactOp::Updated,
                    execution: d.execution.clone(),
                    post: Some(draft_ref.clone()),
                    patch: Some(patch),
                    ..ImpactEvent::default()
                },
            ];
            for m in moves {
                let mut patch = serde_json::Map::new();
                patch.insert("state".into(), json!({"from": m.from, "to": m.to}));
                evs.push(ImpactEvent {
                    change: c.id.clone(),
                    impact: r.cn.id.clone(),
                    op: ImpactOp::Transitioned,
                    execution: d.execution.clone(),
                    post: Some(draft_ref.clone()),
                    patch: Some(patch),
                    ..ImpactEvent::default()
                });
            }
            self.emit(evs).await?;
            if !conflicts.is_empty() {
                left.push(format!("{} ({})", r.cn.key, conflicts.join(", ")));
            }
            done.push((r.cn, conflicts));
        }
        self.review_rebased(id, done, &why).await?;
        Ok((target, left))
    }

    /// Moves the reference baseline of a change with no branch of its own yet (Go's `moveBaseline`), journaled as a
    /// rebase is.
    async fn move_baseline(&mut self, id: &ChangeId, to: &crate::model::BaselineId) -> Result<()> {
        let mut c = self.change_head(id).await?;
        self.header_entry(
            id,
            json!({HEADER_BASELINE_ID: {"from": c.baseline_id.to_string(), "to": to.to_string()}}),
        )
        .await?;
        c.baseline_id = to.clone();
        self.put_change(&c).await
    }
}

/// The conflict a commit stops on when the rebase of its drafts left some (Go's message of `rebaseDrafts`).
pub(crate) fn rebase_conflict(id: &ChangeId, target: &str, left: &[String]) -> GraphError {
    GraphError::conflict(format!(
        "change {id}: its drafts were rebased onto the head of {target}, with conflicts on {}: settle them, review them and commit again",
        left.join(", ")
    ))
}

/// The moves that bring a rebased draft from the head's state (theirs) to the state it ends in (Go's `rebasedMoves`);
/// none: a conflict of states.
fn rebased_moves(
    lc: &Lifecycle,
    events: &[ImpactEvent],
    impact: &str,
    base: &str,
    ours: &str,
    theirs: &str,
) -> Option<Vec<StateMove>> {
    let norm = |s: &str| {
        if s.is_empty() {
            lc.initial.clone()
        } else {
            s.to_string()
        }
    };
    let allowed = |from: &str, to: &str| lc.transitions.iter().any(|t| t.from == from && t.to == to);
    let (base, ours, theirs) = (norm(base), norm(ours), norm(theirs));
    if ours == base || ours == theirs {
        return Some(Vec::new());
    }
    if theirs == base {
        // the moves the change recorded since the draft was checked out, when they start from that state
        let mut recorded: Vec<StateMove> = Vec::new();
        for e in events.iter().filter(|e| e.impact == impact && e.flow.is_empty()) {
            if e.starts_draft() {
                recorded.clear();
                continue;
            }
            if e.op != ImpactOp::Transitioned {
                continue;
            }
            let Some(st) = e.patch.as_ref().and_then(|p| p.get("state")).filter(|v| !v.is_null()) else {
                continue;
            };
            let from = st.get("from").and_then(Value::as_str).unwrap_or_default();
            let to = st.get("to").and_then(Value::as_str).unwrap_or_default();
            recorded.push(StateMove {
                from: norm(from),
                to: to.to_string(),
            });
        }
        if recorded.first().is_some_and(|m| m.from == theirs) && recorded.last().is_some_and(|m| m.to == ours) {
            let mut at = theirs.clone();
            for m in &recorded {
                if m.from != at || !allowed(&at, &m.to) {
                    return None;
                }
                at = m.to.clone();
            }
            return Some(recorded);
        }
    }
    allowed(&theirs, &ours).then(|| vec![StateMove { from: theirs, to: ours }])
}

/// Whether every node the target moved since the fork is one the branch holds a version of deriving from the
/// target's (Go's `overTarget`): the branch then stands over the target as it is.
pub(crate) async fn over_target<T: crate::store::ReadTx>(
    tx: &T,
    ids: &[NodeId],
    fork: &std::collections::HashMap<NodeId, Version>,
    into: &std::collections::HashMap<NodeId, Version>,
    from: &std::collections::HashMap<NodeId, Version>,
) -> Result<bool> {
    for id in ids {
        let (fv, tv) = (fork.get(id), into.get(id));
        if fv == tv {
            continue;
        }
        let (Some(&v), Some(&tv)) = (from.get(id), tv) else {
            return Ok(false);
        };
        if v == tv {
            continue;
        }
        match super::branchmerge::common_ancestor(tx, id, v, tv).await? {
            Some(a) if a.version == tv => {}
            _ => return Ok(false),
        }
    }
    Ok(true)
}
