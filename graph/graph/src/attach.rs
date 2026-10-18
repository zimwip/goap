//! Attaching and detaching a change (ADR 0106). A methodology may integrate the changes of others
//! (`integrates`), a change of it becoming the parent of the changes it integrates. A change created elsewhere is
//! attached to it later: it becomes a sub-change (ADR 0103), forked from the parent's branch (what it committed rebased
//! onto the head of that branch) and merged into it, its guardian chain the parent's plus its own methodology.
//! Detached, it is a root change again, its chain recomputed for its project.

use std::collections::{BTreeMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    BRANCH_OPEN, Branch, CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, GuardianRef, HeaderEdit,
    HeaderValue, INTENT_DERIVE, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, branch_of,
    change_branch_origin, header_entry, namespace_of,
};
use goap_operations as operations;
use serde_json::Value;

use crate::api::NewChange;
use crate::changebranch::{change_branch_name, own_branch};
use crate::dependency::ancestor_ids;
use crate::merge::branch_head;
use crate::rebase::has_commits;
use crate::repo::Tx;
use crate::transition::chain_under;
use crate::{Error, ErrorKind, Graph, Result};

/// Fields of a change.updated log entry that record an attachment or a detachment.
pub const HEADER_PARENT_ID: &str = "parentId";
pub const HEADER_GUARDIANS: &str = "guardians";

/// guardiansValue is a guardian chain as a field of a header edit (a Go nil slice: null).
pub(crate) fn guardians_value(chain: &[GuardianRef]) -> Value {
    if chain.is_empty() {
        Value::Null
    } else {
        serde_json::to_value(chain).unwrap_or(Value::Null)
    }
}

impl Graph {
    /// AttachChange makes a root change a sub-change of another change, whose methodology integrates its own (ADR
    /// 0106): both open, in one namespace, the change acting in the parent's project or one below it, held by the
    /// parent's unit or one below it. Its branch is forked again from the head of the parent's branch: what it
    /// committed is rebased there (Conflict when that leaves conflicts: nothing changes then), and its guardian chain
    /// becomes the parent's plus its own methodology.
    ///
    /// goap:operation change.attach doer
    pub async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change> {
        self.allow(ctx, parent, operations::CHANGE_ATTACH).await?;
        let (c, p) = {
            let mut tx = self.begin(ctx).await?;
            let r = self.attachable_tx(ctx, &mut tx, id, parent).await?;
            tx.commit(ctx).await?;
            r
        };
        if let Some(lcs) = &self.lifecycles
            && !c.methodology.is_empty()
            && !lcs
                .integrates(ctx, &p.methodology_ref(), &c.methodology)
                .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "methodology {:?} does not integrate the changes of {:?}",
                    p.methodology, c.methodology
                ),
            ));
        }
        let mut tx = self.change_tx(ctx, id).await?;
        // the change joins the parent's tree: both locks
        tx.lock_change(ctx, parent).await?;
        let (mut cur, pc) = self.attachable_tx(ctx, &mut tx, id, parent).await?;
        let pc = self.parent_branch(ctx, &mut tx, pc).await?;
        let chain = chain_under(&pc, &cur.methodology, own_ref(&cur).as_ref());
        let mut fields = BTreeMap::new();
        fields.insert(
            HEADER_PARENT_ID.to_string(),
            HeaderValue {
                from: Value::Null,
                to: Value::String(pc.id.to_string()),
            },
        );
        fields.insert(
            HEADER_GUARDIANS.to_string(),
            HeaderValue {
                from: guardians_value(&cur.guardians),
                to: guardians_value(&chain),
            },
        );
        self.header_entry(ctx, &mut tx, &cur.id, fields).await?;
        cur.parent_id = pc.id.clone();
        cur.guardians = chain;
        let c = self.reparent(ctx, &mut tx, cur, &pc.branch).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    /// DetachChange gives a sub-change back as a root change (ADR 0106: deferred, rejected by the change integrating
    /// it), still open and not merged: its branch is forked again from the head of the branch its root ancestor merges
    /// into (what it committed rebased there; Conflict when that leaves conflicts), its guardian chain recomputed for
    /// its methodology and project.
    ///
    /// goap:operation change.detach doer
    pub async fn detach_change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        let c = self.change(ctx, id).await?;
        if c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {id} is not a sub-change"),
            ));
        }
        self.allow(ctx, &c.parent_id, operations::CHANGE_DETACH)
            .await?;
        let chain = self
            .guardian_chain(
                ctx,
                &NewChange {
                    methodology: c.methodology.clone(),
                    project_id: c.project_id.clone(),
                    ..Default::default()
                },
                &c.methodology_version,
            )
            .await?;
        let mut tx = self.change_tx(ctx, id).await?;
        let mut cur = tx.change(ctx, id).await?;
        if cur.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is not a sub-change"),
            ));
        }
        open_to_move(&cur)?;
        let target = self.root_target(ctx, &mut tx, &cur).await?;
        let mut fields = BTreeMap::new();
        fields.insert(
            HEADER_PARENT_ID.to_string(),
            HeaderValue {
                from: Value::String(cur.parent_id.to_string()),
                to: Value::String(String::new()),
            },
        );
        fields.insert(
            HEADER_GUARDIANS.to_string(),
            HeaderValue {
                from: guardians_value(&cur.guardians),
                to: guardians_value(&chain),
            },
        );
        self.header_entry(ctx, &mut tx, &cur.id, fields).await?;
        cur.parent_id = ChangeID::default();
        cur.guardians = chain;
        let c = self.reparent(ctx, &mut tx, cur, &target).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }

    pub(crate) async fn attachable_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<(Change, Change)> {
        let c = tx.change(ctx, id).await?;
        let p = tx.change(ctx, parent).await?;
        if c.id == p.id {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                "a change is not attached to itself",
            ));
        }
        if !c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} is already a sub-change of {}: detach it first",
                    c.id, c.parent_id
                ),
            ));
        }
        if namespace_of(&c.namespace) != namespace_of(&p.namespace) {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "a sub-change acts on the namespace of its parent ({}), change {} acts on {}",
                    namespace_of(&p.namespace),
                    c.id,
                    namespace_of(&c.namespace)
                ),
            ));
        }
        open_to_move(&c)?;
        if p.status != CHANGE_DRAFT && p.status != CHANGE_ACTIVE {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("parent change {} is {}", p.id, p.status),
            ));
        }
        // the parent must not be under the change: an attachment never makes a cycle
        let mut seen = HashSet::new();
        let mut a = p.parent_id.clone();
        while !a.is_empty() && !seen.contains(&a) {
            if a == c.id {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("change {} is an ancestor of {}", c.id, p.id),
                ));
            }
            seen.insert(a.clone());
            a = tx.change(ctx, &a).await?.parent_id;
        }
        // a dependency never joins an ancestor and its sub-change (ADR 0108)
        let up = ancestor_ids(ctx, tx, &p).await?;
        for a in std::iter::once(p.id.clone()).chain(up) {
            let ac = tx.change(ctx, &a).await?;
            if c.depends_on.contains(&a) || ac.depends_on.contains(&c.id) {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!(
                        "change {} and {a} depend on one another: remove the dependency before attaching",
                        c.id
                    ),
                ));
            }
        }
        if !self
            .within(ctx, tx, STRUCTURE_PROJECT, &c.project_id, &p.project_id)
            .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "project {} is not part of {}, the project of change {}",
                    c.project_id, p.project_id, p.id
                ),
            ));
        }
        if !self
            .within(ctx, tx, STRUCTURE_ORGANISATION, &c.owner_org, &p.owner_org)
            .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "unit {} is not part of {}, the unit of change {}",
                    c.owner_org, p.owner_org, p.id
                ),
            ));
        }
        Ok((c, p))
    }

    /// parentBranch gives the parent the branch of its own its sub-changes are forked from and merged into.
    pub(crate) async fn parent_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        p: Change,
    ) -> Result<Change> {
        if own_branch(ctx, tx, &p).await?.is_some() {
            return Ok(p);
        }
        Ok(self.ensure_own_branch(ctx, tx, p).await?.0)
    }

    /// rootTarget is the branch the root ancestor of a change merges into.
    pub(crate) async fn root_target(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<String> {
        let mut root = c.clone();
        let mut seen = HashSet::new();
        while !root.parent_id.is_empty() && !seen.contains(&root.parent_id) {
            seen.insert(root.parent_id.clone());
            root = tx.change(ctx, &root.parent_id).await?;
        }
        Ok(match own_branch(ctx, tx, &root).await? {
            Some(own) => branch_of(&own.parent).to_string(),
            None => branch_of(&root.branch).to_string(),
        })
    }

    /// reparent forks the branch of a change again from the head of the branch target, the change's header (parent,
    /// chain) already set: what it committed is rebased there, conflicts refusing the move; a change that committed
    /// nothing is forked from that head; one without a branch of its own gets one.
    pub(crate) async fn reparent(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        mut c: Change,
        target: &str,
    ) -> Result<Change> {
        let head = branch_head(ctx, tx, &c.namespace, target).await?;
        let mut own = match own_branch(ctx, tx, &c).await? {
            Some(b) => b,
            None => Branch {
                name: change_branch_name(&c.id),
                namespace: c.namespace.clone(),
                origin: change_branch_origin(&c.id),
                intent: INTENT_DERIVE,
                status: BRANCH_OPEN.to_string(),
                created_at: self.now(),
                ..Default::default()
            },
        };
        own.parent = target.to_string();
        if !has_commits(&c) {
            own.fork_baseline = head.id.clone();
            own.head = head.id.clone();
            c.baseline_id = head.id.clone();
        }
        tx.put_branch(ctx, own.clone()).await?;
        c.branch = own.name.clone();
        tx.put_change(ctx, c.clone()).await?;
        if !has_commits(&c) {
            return Ok(c);
        }
        let res = self.rebase_committed_tx(ctx, tx, &c, own).await?;
        if res.changed() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} {}: settle it on its own before it moves",
                    c.id,
                    res.summary()
                ),
            ));
        }
        tx.change(ctx, &c.id).await
    }

    /// headerEntry journals an edit of the header of a change (a change.updated log entry).
    pub(crate) async fn header_entry(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        fields: BTreeMap<String, HeaderValue>,
    ) -> Result<()> {
        let e = header_entry(
            &self.new_id(),
            id,
            &self.caller_of(ctx),
            self.now(),
            &HeaderEdit { fields },
        )?;
        tx.append_log(ctx, e).await?;
        Ok(())
    }
}

/// openToMove refuses a change that is no longer open (merged, abandoned, waiting for the resolutions of a merge).
pub(crate) fn open_to_move(c: &Change) -> Result<()> {
    if c.status == CHANGE_DRAFT || c.status == CHANGE_ACTIVE {
        return Ok(());
    }
    Err(Error::wrap(
        ErrorKind::Conflict,
        format!(
            "change {} is {}: only an open change is attached or detached",
            c.id, c.status
        ),
    ))
}

/// ownRef is the reference of the change's own methodology in its chain (its version kept), None when absent.
pub(crate) fn own_ref(c: &Change) -> Option<GuardianRef> {
    if !c.methodology_version.is_empty() {
        return Some(GuardianRef {
            methodology: c.methodology.clone(),
            version: c.methodology_version.clone(),
        });
    }
    c.guardians
        .iter()
        .find(|g| g.methodology == c.methodology)
        .cloned()
}
