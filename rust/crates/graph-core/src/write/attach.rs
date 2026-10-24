//! Attaching and detaching a change (Go's `attach.go`, ADR 0106): a root change becomes a sub-change of another one,
//! forked again from its parent's branch with what it committed rebased there; detached, it is a root change again,
//! forked from the head of the branch its root ancestor merges into. With no registry no methodology is asked whether
//! it integrates another, and a detached change's guardian chain is none.
use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};
use serde_json::{Value, json};

use super::{
    change::{INTENT_DERIVE, STATUS_ACTIVE, STATUS_DRAFT, change_branch_name, change_branch_origin},
    events::{CHANGE_ATTACHED, CHANGE_DETACHED},
    rebase::{Onto, main_impacts},
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::{Change, GuardianRef},
    model::{Branch, ChangeId, branch_of, namespace_of},
    read,
    store::WriteTx,
};

/// The field of a header edit recording an attachment or a detachment (`graph.HeaderParentID`).
pub const HEADER_PARENT_ID: &str = "parentId";
/// The field of a header edit recording the guardian chain it gives (`graph.HeaderGuardians`).
pub const HEADER_GUARDIANS: &str = "guardians";
const BRANCH_OPEN: &str = "open";

/// A guardian chain as a header edit records it: none is null.
fn chain_value(chain: &[GuardianRef]) -> Value {
    if chain.is_empty() { Value::Null } else { json!(chain) }
}

/// The chain of a change of a methodology under a parent (Go's `chainUnder`): the parent's chain (its own methodology
/// when it has none) then the methodology, unless it is the last one already.
fn chain_under(parent: &Change, methodology: &str, own: Option<GuardianRef>) -> Vec<GuardianRef> {
    let mut chain = parent.guardians.clone();
    if chain.is_empty() && !parent.methodology.is_empty() {
        chain.push(GuardianRef {
            methodology: parent.methodology.clone(),
            version: String::new(),
        });
    }
    if !methodology.is_empty() && chain.last().is_none_or(|g| g.methodology != methodology) {
        chain.push(own.unwrap_or_else(|| GuardianRef {
            methodology: methodology.to_string(),
            version: String::new(),
        }));
    }
    chain
}

/// The reference of the change's own methodology in its chain, its version kept (Go's `ownRef`).
fn own_ref(c: &Change) -> Option<GuardianRef> {
    if !c.methodology_version.is_empty() {
        return Some(GuardianRef {
            methodology: c.methodology.clone(),
            version: c.methodology_version.clone(),
        });
    }
    c.guardians.iter().find(|g| g.methodology == c.methodology).cloned()
}

/// A change still open may be attached or detached (Go's `openToMove`).
fn open_to_move(c: &Change) -> Result<()> {
    if c.status == STATUS_DRAFT || c.status == STATUS_ACTIVE {
        return Ok(());
    }
    Err(GraphError::conflict(format!(
        "change {} is {}: only an open change is attached or detached",
        c.id, c.status
    )))
}

impl<S: WriteTx> Tx<'_, S> {
    /// Makes a root change a sub-change of another open change of its namespace (Go's `AttachChange`): acting in the
    /// parent's project or one below it, held by its unit or one below it; its branch forked again from the parent's,
    /// what it committed rebased there (a conflict refuses the move), its chain the parent's plus its methodology.
    pub async fn attach_change(&mut self, id: &ChangeId, parent: &ChangeId) -> Result<(Change, i64)> {
        self.attachable(id, parent).await?;
        self.lock_change(id).await?;
        self.lock_change(parent).await?;
        let (mut cur, mut pc) = self.attachable(id, parent).await?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        if self.own_branch(&pc).await?.is_none() {
            pc = self.ensure_own_branch(pc).await?;
        }
        let chain = chain_under(&pc, &cur.methodology, own_ref(&cur));
        self.header_entry(
            &cur.id,
            json!({
                HEADER_PARENT_ID: {"to": pc.id.to_string()},
                HEADER_GUARDIANS: {"from": chain_value(&cur.guardians), "to": chain_value(&chain)},
            }),
        )
        .await?;
        cur.parent_id = pc.id.clone();
        cur.guardians = chain;
        let (c, seq) = self.reparent(cur, &pc.branch, seq).await?;
        self.raise(CHANGE_ATTACHED, &c, None, "", &[]);
        Ok((c, seq))
    }

    /// Gives a sub-change back as a root change, still open (Go's `DetachChange`): its branch forked again from the
    /// head of the branch its root ancestor merges into, what it committed rebased there.
    pub async fn detach_change(&mut self, id: &ChangeId) -> Result<(Change, i64)> {
        let c = self.change_head(id).await?;
        if c.parent_id.is_empty() {
            return Err(GraphError::invalid(format!("change {id} is not a sub-change")));
        }
        self.lock_change(id).await?;
        let mut cur = self.change_head(id).await?;
        if cur.parent_id.is_empty() {
            return Err(GraphError::conflict(format!("change {id} is not a sub-change")));
        }
        open_to_move(&cur)?;
        let seq = super::work::fold_change(self.store, id).await?.seq;
        let target = self.root_target(&cur).await?;
        self.header_entry(
            &cur.id,
            json!({
                HEADER_PARENT_ID: {"from": cur.parent_id.to_string(), "to": ""},
                HEADER_GUARDIANS: {"from": chain_value(&cur.guardians), "to": Value::Null},
            }),
        )
        .await?;
        let left = cur.parent_id.clone();
        cur.parent_id = ChangeId::default();
        cur.guardians = Vec::new();
        let (c, seq) = self.reparent(cur, &target, seq).await?;
        // the event names the change it left
        let mut ev = c.clone();
        ev.parent_id = left;
        self.raise(CHANGE_DETACHED, &ev, None, "", &[]);
        Ok((c, seq))
    }

    /// The checks of an attachment (Go's `attachableTx`): the change and its parent as they stand.
    async fn attachable(&mut self, id: &ChangeId, parent: &ChangeId) -> Result<(Change, Change)> {
        let c = self.change_head(id).await?;
        let p = self.change_head(parent).await?;
        if c.id == p.id {
            return Err(GraphError::invalid("a change is not attached to itself"));
        }
        if !c.parent_id.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {} is already a sub-change of {}: detach it first",
                c.id, c.parent_id
            )));
        }
        if namespace_of(&c.namespace) != namespace_of(&p.namespace) {
            return Err(GraphError::invalid(format!(
                "a sub-change acts on the namespace of its parent ({}), change {} acts on {}",
                namespace_of(&p.namespace),
                c.id,
                namespace_of(&c.namespace)
            )));
        }
        open_to_move(&c)?;
        if p.status != STATUS_DRAFT && p.status != STATUS_ACTIVE {
            return Err(GraphError::conflict(format!("parent change {} is {}", p.id, p.status)));
        }
        // the parent must not be under the change: an attachment never makes a cycle
        let mut up = Vec::new();
        let mut a = p.parent_id.clone();
        while !a.is_empty() && !up.contains(&a) {
            if a == c.id {
                return Err(GraphError::invalid(format!(
                    "change {} is an ancestor of {}",
                    c.id, p.id
                )));
            }
            up.push(a.clone());
            a = self.change_head(&a).await?.parent_id;
        }
        // a dependency never joins an ancestor and its sub-change (ADR 0108)
        for a in std::iter::once(p.id.clone()).chain(up) {
            let ac = self.change_head(&a).await?;
            if c.depends_on.contains(&a) || ac.depends_on.contains(&c.id) {
                return Err(GraphError::invalid(format!(
                    "change {} and {a} depend on one another: remove the dependency before attaching",
                    c.id
                )));
            }
        }
        if !self.within(STRUCTURE_PROJECT, &c.project_id, &p.project_id).await? {
            return Err(GraphError::invalid(format!(
                "project {} is not part of {}, the project of change {}",
                c.project_id, p.project_id, p.id
            )));
        }
        if !self.within(STRUCTURE_ORGANISATION, &c.owner_org, &p.owner_org).await? {
            return Err(GraphError::invalid(format!(
                "unit {} is not part of {}, the unit of change {}",
                c.owner_org, p.owner_org, p.id
            )));
        }
        Ok((c, p))
    }

    /// The branch the root ancestor of a change merges into (Go's `rootTarget`).
    async fn root_target(&mut self, c: &Change) -> Result<String> {
        let mut root = c.clone();
        let mut seen = Vec::new();
        while !root.parent_id.is_empty() && !seen.contains(&root.parent_id) {
            seen.push(root.parent_id.clone());
            root = self.change_head(&root.parent_id).await?;
        }
        Ok(match self.own_branch(&root).await? {
            Some(own) => branch_of(&own.parent).to_string(),
            None => branch_of(&root.branch).to_string(),
        })
    }

    /// Forks the branch of a change again from the head of the branch target, its header already set (Go's
    /// `reparent`): what it committed rebased there, a conflict refusing the move; a change that committed nothing is
    /// forked from that head, one without a branch of its own gets one. The change comes back with the position it
    /// was read at: seq, else, rebased, the position its log stands at now.
    async fn reparent(&mut self, mut c: Change, target: &str, seq: i64) -> Result<(Change, i64)> {
        let tb = read::branch(self.store, &c.namespace, target).await?;
        let mut own = match self.own_branch(&c).await? {
            Some(b) => b,
            None => Branch {
                ephemeral: true,
                name: change_branch_name(&c.id),
                namespace: c.namespace.clone(),
                origin: change_branch_origin(&c.id),
                intent: INTENT_DERIVE.into(),
                status: BRANCH_OPEN.into(),
                created_at: Some(self.env.now()),
                ..Branch::default()
            },
        };
        let before = own.clone();
        own.parent = target.to_string();
        self.view(&c.id).await?;
        let commits = main_impacts(&self.views[&c.id].fold.impacts)
            .iter()
            .any(|cn| cn.committed.is_some());
        if !commits {
            let to = Onto {
                head: if tb.ephemeral {
                    Default::default()
                } else {
                    read::branch_head(self.store, &tb.namespace, &tb.name).await?.id
                },
                parent: tb,
            };
            own.fork_seq = 0;
            own.fork_baseline = to.head.clone();
            if to.parent.ephemeral {
                (own.fork_baseline, own.fork_seq) = super::subchange::fork_point(self, &to.parent).await?;
            }
            c.baseline_id = own.fork_baseline.clone();
        }
        self.put_branch(&own).await?;
        c.branch = own.name.clone();
        self.put_change(&c).await?;
        if !commits {
            return Ok((c, seq));
        }
        let res = self.rebase_onto(&c, own, &before, false).await?;
        if res.changed() {
            return Err(GraphError::conflict(format!(
                "change {} {}: settle it on its own before it moves",
                c.id,
                res.summary()
            )));
        }
        let seq = super::work::fold_change(self.store, &c.id).await?.seq;
        Ok((self.change_head(&c.id).await?, seq))
    }
}
