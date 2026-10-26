//! Sub-changes (Go's `subchange.go`, ADR 0103): a change opened under another one, on a branch forked from its
//! parent's, and merged into it.
use std::collections::HashSet;

use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};

use serde_json::Value;

use super::{
    change::{INTENT_DERIVE, NewChange, STATUS_ACTIVE, STATUS_DRAFT, change_branch_name, change_branch_origin},
    events,
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::{Change, ChangesFilter, STATUS_APPLIED},
    impact::{ChangeImpact, INTENT_MODIFIED, ImpactEvent, ImpactOp, REVIEW_PROPOSED},
    lnk::Url,
    model::{BRANCH_OPEN, BaselineId, Branch, ChangeId, MAIN_BRANCH, Target, namespace_of},
    read,
    store::WriteTx,
};

/// Who splits a change by its owners.
const SPLIT_BY_OWNER: &str = "graph.split_by_owner";

/// The parent statuses that take no more sub-change.
const CLOSED: [&str; 3] = ["applied", "abandoned", "committed"];

/// Where a branch forked from the branch of a change starts (Go's `forkPoint`, ADR 0103, 0140): the lasting baseline
/// under the chain, and the order of commit up to which the commits of the chain are in its fork, allocated now.
pub(crate) async fn fork_point<S: WriteTx>(tx: &mut Tx<'_, S>, parent: &Branch) -> Result<(BaselineId, i64)> {
    if !parent.ephemeral {
        let h = read::branch_head(tx.store, &parent.namespace, &parent.name).await?;
        return Ok((h.id, 0));
    }
    let seq = tx.store.next_commit().await?;
    Ok((parent.fork_baseline.clone(), seq))
}

impl<S: WriteTx> Tx<'_, S> {
    /// Applies the rules of a sub-change to `c` (Go's `prepareSubChange`): its parent must be open and is given a
    /// branch of its own when it has none, the namespace is the parent's, and the change is forked from the head of
    /// the parent's branch. Its unit and its project must be inside the parent's.
    pub(crate) async fn prepare_sub_change(&mut self, c: &mut Change, inp: &mut NewChange) -> Result<()> {
        if c.parent_id.is_empty() {
            return Ok(());
        }
        let mut parent = self.change_head(&c.parent_id).await?;
        if CLOSED.contains(&parent.status.as_str()) {
            return Err(GraphError::conflict(format!(
                "parent change {} is {}",
                parent.id, parent.status
            )));
        }
        let own = match self.own_branch(&parent).await? {
            Some(b) => b,
            None => {
                // the branch of the parent is where its sub-changes are forked from and merged into
                parent = self.ensure_own_branch(parent).await?;
                read::branch(self.store, &parent.namespace, &parent.branch).await?
            }
        };
        if !inp.namespace.is_empty() && namespace_of(&inp.namespace) != namespace_of(&parent.namespace) {
            return Err(GraphError::invalid(format!(
                "a sub-change acts on the namespace of its parent ({})",
                namespace_of(&parent.namespace)
            )));
        }
        c.namespace = namespace_of(&parent.namespace).to_string();
        c.branch = own.name.clone();
        inp.own_branch = true;
        if c.baseline_id.is_empty() {
            // the lasting baseline under the branch of the parent: the branch of a change holds none (ADR 0140)
            c.baseline_id = own.fork_baseline.clone();
        }
        if c.methodology.is_empty() {
            c.methodology = parent.methodology.clone();
        }
        if inp.methodology.is_empty() || inp.methodology == parent.methodology {
            c.goal = parent.goal.clone();
        }
        if c.owner_org.is_zero() {
            c.owner_org = parent.owner_org.clone();
        } else if c.owner_org != parent.owner_org
            && !self
                .within(STRUCTURE_ORGANISATION, &c.owner_org, &parent.owner_org)
                .await?
        {
            return Err(GraphError::invalid(format!(
                "unit {} is not part of {}, the unit of the parent change",
                c.owner_org, parent.owner_org
            )));
        }
        if c.project_id.is_zero() {
            c.project_id = parent.project_id.clone();
        } else if c.project_id != parent.project_id
            && !self
                .within(STRUCTURE_PROJECT, &c.project_id, &parent.project_id)
                .await?
        {
            return Err(GraphError::invalid(format!(
                "project {} is not part of {}, the project of the parent change",
                c.project_id, parent.project_id
            )));
        }
        Ok(())
    }

    /// Splits a change along its organisation (Go's `SplitByOwner`, ADR 0024, 0054, 0103): one sub-change per unit
    /// below the one holding the change that owns nodes the main flow has a candidate impact on (the owner of its pre
    /// version), the candidate moved to it (withdrawn from the parent, copied derived from it). A unit that has an open
    /// sub-change already is left as it is. The sub-changes created.
    pub async fn split_by_owner(&mut self, id: &ChangeId) -> Result<Vec<Change>> {
        let parent = self.change_head(id).await?;
        let mut have = HashSet::new();
        let f = ChangesFilter {
            parent: id.clone(),
            ..ChangesFilter::default()
        };
        for sc in self.store.change_heads(&f).await? {
            if sc.status != STATUS_APPLIED && sc.status != "abandoned" {
                have.insert(sc.owner_org);
            }
        }
        let view = self.view(id).await?;
        let seen: HashSet<String> = view.seen("").into_iter().map(|cn| cn.id).collect();
        let impacts = view.fold.impacts.clone();
        // the units in the order their first candidate comes, each with its name and candidates
        let mut groups: Vec<(Url, String, Vec<ChangeImpact>)> = Vec::new();
        for cn in impacts {
            if !seen.contains(&cn.id) && cn.items.is_empty() {
                continue; // a candidate of a flow, or replaced by one
            }
            let Some(pre) = cn
                .pre
                .as_ref()
                .filter(|_| cn.intent == INTENT_MODIFIED && cn.post.is_none() && !cn.superseded)
            else {
                continue; // only a candidate moves: a node the change works on already stays with it
            };
            let pre = read::stored_node(self.store, pre).await?;
            if pre.responsible == parent.owner_org {
                continue; // the unit holding the change owns it
            }
            let unit = self.structure_node(STRUCTURE_ORGANISATION, &pre.responsible).await?;
            if !self
                .within(STRUCTURE_ORGANISATION, &pre.responsible, &parent.owner_org)
                .await?
            {
                continue; // owned outside the unit holding the change: nothing to delegate to
            }
            match groups.iter_mut().find(|g| g.0 == pre.responsible) {
                Some(g) => g.2.push(cn),
                None => {
                    let name = unit
                        .props
                        .as_ref()
                        .and_then(|p| p.get("name"))
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    let name = if name.is_empty() {
                        unit.key.clone()
                    } else {
                        name.to_string()
                    };
                    groups.push((pre.responsible.clone(), name, vec![cn]));
                }
            }
        }
        let mut created = Vec::new();
        for (org, name, nodes) in groups {
            if have.contains(&org) {
                continue;
            }
            let mut inp = NewChange {
                title: format!("{} / {name}", parent.title),
                intent: parent.intent.clone(),
                methodology: parent.methodology.clone(),
                parent_id: id.clone(),
                owner_org: org.to_string(),
                ..NewChange::default()
            };
            let mut sub = Change {
                id: ChangeId(self.env.new_id()),
                title: inp.title.clone(),
                intent: inp.intent.clone(),
                methodology: inp.methodology.clone(),
                status: STATUS_DRAFT.into(),
                parent_id: id.clone(),
                owner_org: org,
                created_at: Some(self.env.now()),
                ..Change::default()
            };
            self.prepare_sub_change(&mut sub, &mut inp).await?;
            self.scope_change(&mut sub).await?;
            let fork = read::header_of(self.store, &sub.baseline_id).await?;
            // forked as a sub-change is created (`create_change`): at the point of the branch of its parent
            let pb = read::branch(self.store, &sub.namespace, &sub.branch).await?;
            let mut own = Branch {
                ephemeral: true,
                name: change_branch_name(&sub.id),
                namespace: sub.namespace.clone(),
                parent: sub.branch.clone(),
                fork_baseline: fork.id.clone(),
                origin: change_branch_origin(&sub.id),
                intent: INTENT_DERIVE.into(),
                status: BRANCH_OPEN.into(),
                created_at: Some(self.env.now()),
                ..Branch::default()
            };
            if pb.ephemeral {
                (own.fork_baseline, own.fork_seq) = fork_point(self, &pb).await?;
            }
            self.put_branch(&own).await?;
            sub.branch = own.name;
            self.put_change(&sub).await?;
            for cn in nodes {
                // the candidate moves to the sub-change (ADR 0103): withdrawn from the parent, it comes back to it with
                // the version the sub-change commits, when the sub-change merges
                self.emit(vec![ImpactEvent {
                    change: id.clone(),
                    impact: cn.id.clone(),
                    op: ImpactOp::Withdrawn,
                    by: SPLIT_BY_OWNER.into(),
                    ..ImpactEvent::default()
                }])
                .await?;
                let cp = ChangeImpact {
                    id: self.env.new_id(),
                    key: cn.key.clone(),
                    typ: cn.typ.clone(),
                    intent: INTENT_MODIFIED.into(),
                    rationale: cn.rationale.clone(),
                    pre: cn.pre.clone(),
                    review: REVIEW_PROPOSED.into(),
                    produced_by: SPLIT_BY_OWNER.into(),
                    derived_from: vec![cn.id.clone()],
                    created_at: self.env.now(),
                    ..ChangeImpact::default()
                };
                self.emit(vec![ImpactEvent {
                    change: sub.id.clone(),
                    impact: cp.id.clone(),
                    op: ImpactOp::Proposed,
                    by: SPLIT_BY_OWNER.into(),
                    state: Some(cp),
                    ..ImpactEvent::default()
                }])
                .await?;
            }
            sub.status = STATUS_ACTIVE.into();
            self.put_change(&sub).await?;
            self.raise(events::CHANGE_CREATED, &sub, None, "", &[]);
            created.push(sub);
        }
        Ok(created)
    }

    /// The branch a change owns, when it has one (Go's `ownBranch`).
    pub(crate) async fn own_branch(&self, c: &Change) -> Result<Option<Branch>> {
        if crate::model::branch_of(&c.branch) == MAIN_BRANCH {
            return Ok(None);
        }
        let b = read::branch(self.store, &c.namespace, &c.branch).await?;
        Ok((b.origin == super::change::change_branch_origin(&c.id)).then_some(b))
    }

    /// Refuses the check out of a stored node by a change whose project does not own it (Go's `checkOwned`, ADR 0161
    /// §3): the node's project is the change's or one below it. The open namespaces, a node with no project or a change
    /// with none are not judged.
    pub(crate) async fn check_owned(&self, c: &Change, n: &crate::model::Node) -> Result<()> {
        if crate::reach::OPEN_NAMESPACES.contains(&namespace_of(&n.namespace))
            || n.project.is_zero()
            || c.project_id.is_zero()
            || n.project.key == c.project_id.key
        {
            return Ok(());
        }
        if self.within(STRUCTURE_PROJECT, &n.project, &c.project_id).await? {
            return Ok(());
        }
        Err(GraphError::new(
            crate::error::Kind::Forbidden,
            format!(
                "node {} belongs to project {}, which change {} (project {}) does not own: permission denied",
                n.key, n.project.key, c.id, c.project_id.key
            ),
        ))
    }

    /// Whether a node of a structure is the other or below it (Go's `within`): walked up its parent links on main.
    pub(crate) async fn within(&self, kind: &str, u: &Url, of: &Url) -> Result<bool> {
        let st = self
            .catalog()
            .structure(kind)
            .cloned()
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))?;
        if let Some(p) = self.store.platform() {
            return Ok(p.structure_chain(self.catalog(), kind, &u.key).contains(&of.key));
        }
        let mut seen = HashSet::new();
        let mut cur = u.key.clone();
        while !cur.is_empty() && seen.insert(cur.clone()) {
            if cur == of.key {
                return Ok(true);
            }
            let id = self
                .store
                .node_id_by_key(&st.namespace, &cur)
                .await?
                .ok_or_else(|| GraphError::not_found(format!("node {cur}")))?;
            let n = self
                .store
                .latest_on(&id, MAIN_BRANCH)
                .await?
                .ok_or_else(|| GraphError::not_found(format!("node {id} on branch {MAIN_BRANCH}")))?;
            cur = String::new();
            for l in self.store.links_from(&[n.node_ref()], None).await? {
                if l.typ != st.parent {
                    continue;
                }
                if let Target::Node(to) = &l.to {
                    cur = read::stored_node(self.store, to).await?.key;
                }
                break;
            }
        }
        Ok(false)
    }
}
