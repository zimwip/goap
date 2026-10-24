//! Sub-changes (Go's `subchange.go`, ADR 0103): a change opened under another one, on a branch forked from its
//! parent's, and merged into it.
use std::collections::HashSet;

use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};

use super::{change::NewChange, tx::Tx};
use crate::{
    GraphError, Result,
    change::Change,
    lnk::Url,
    model::{BaselineId, Branch, MAIN_BRANCH, Target, namespace_of},
    read,
    store::WriteTx,
};

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

    /// The branch a change owns, when it has one (Go's `ownBranch`).
    pub(crate) async fn own_branch(&self, c: &Change) -> Result<Option<Branch>> {
        if crate::model::branch_of(&c.branch) == MAIN_BRANCH {
            return Ok(None);
        }
        let b = read::branch(self.store, &c.namespace, &c.branch).await?;
        Ok((b.origin == super::change::change_branch_origin(&c.id)).then_some(b))
    }

    /// Whether a node of a structure is the other or below it (Go's `within`): walked up its parent links on main.
    pub(crate) async fn within(&self, kind: &str, u: &Url, of: &Url) -> Result<bool> {
        let st = self
            .catalog()
            .structure(kind)
            .cloned()
            .ok_or_else(|| GraphError::internal(format!("no {kind} structure in the catalogue")))?;
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
