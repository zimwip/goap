//! Part of `subchange.go` (G1): the rules of a new sub-change and the listing and abandonment of sub-changes (ADR
//! 0103). The checks of a version (`checkVersion`, `checkDraft`, `checkRequiredLinks`) and SplitByOwner belong to
//! later slices (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{
    BRANCH_ABANDONED, CHANGE_ABANDONED, CHANGE_APPLIED, CHANGE_COMMITTED, Change, ChangeID,
    STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, namespace_of,
};

use crate::api::NewChange;
use crate::changebranch::own_branch;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// prepareSubChange applies the rules of a sub-change to c: its parent must be open and is given a branch of its
    /// own when it has none, the namespace is the parent's, and the change is forked from the head of the parent's
    /// branch, the branch it merges into (ADR 0103). Its organisational unit must be inside the unit of the parent.
    pub(crate) async fn prepare_sub_change(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &mut Change,
        input: &mut NewChange,
    ) -> Result<()> {
        if c.parent_id.is_empty() {
            return Ok(());
        }
        let mut parent = tx.change(ctx, &c.parent_id).await?;
        if parent.status == CHANGE_APPLIED
            || parent.status == CHANGE_ABANDONED
            || parent.status == CHANGE_COMMITTED
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("parent change {} is {}", parent.id, parent.status),
            ));
        }
        let own = match own_branch(ctx, tx, &parent).await? {
            Some(b) => b,
            None => {
                // the branch of the parent is where its sub-changes are forked from and merged into
                let (p, own) = self.ensure_own_branch(ctx, tx, parent).await?;
                parent = p;
                own
            }
        };
        if !input.namespace.is_empty()
            && namespace_of(&input.namespace) != namespace_of(&parent.namespace)
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "a sub-change acts on the namespace of its parent ({})",
                    namespace_of(&parent.namespace)
                ),
            ));
        }
        c.namespace = namespace_of(&parent.namespace).to_string();
        c.branch = own.name.clone();
        input.own_branch = true;
        if c.baseline_id.is_empty() {
            let head = branch_head(ctx, tx, &c.namespace, &own.name).await?;
            c.baseline_id = head.id;
        }
        if c.methodology.is_empty() {
            c.methodology = parent.methodology.clone();
        }
        // a sub-change works towards its parent's goal unless it names one of its own, or a methodology other than
        // the parent's (whose default goal CreateChange has set, ADR 0096)
        if input.goal.is_empty()
            && (input.methodology.is_empty() || input.methodology == parent.methodology)
        {
            c.goal = parent.goal.clone();
        }
        if c.owner_org.is_empty() {
            c.owner_org = parent.owner_org.clone();
        } else if c.owner_org != parent.owner_org
            && !self
                .within(
                    ctx,
                    tx,
                    STRUCTURE_ORGANISATION,
                    &c.owner_org,
                    &parent.owner_org,
                )
                .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "unit {} is not part of {}, the unit of the parent change",
                    c.owner_org, parent.owner_org
                ),
            ));
        }
        if c.project_id.is_empty() {
            c.project_id = parent.project_id.clone();
        } else if c.project_id != parent.project_id
            && !self
                .within(
                    ctx,
                    tx,
                    STRUCTURE_PROJECT,
                    &c.project_id,
                    &parent.project_id,
                )
                .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "project {} is not part of {}, the project of the parent change",
                    c.project_id, parent.project_id
                ),
            ));
        }
        Ok(())
    }

    /// SubChanges lists the sub-changes of a change, oldest first.
    pub async fn sub_changes(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Change>> {
        let mut tx = self.begin(ctx).await?;
        let out = sub_changes(ctx, &mut tx, id).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// abandonSubChanges abandons the open sub-changes of a change (and their branches), recursively.
    pub(crate) async fn abandon_sub_changes(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<()> {
        for mut sc in open_sub_changes(ctx, tx, id).await? {
            Box::pin(self.abandon_sub_changes(ctx, tx, &sc.id)).await?;
            if let Some(mut own) = own_branch(ctx, tx, &sc).await? {
                own.status = BRANCH_ABANDONED.to_string();
                tx.put_branch(ctx, own).await?;
            }
            sc.status = CHANGE_ABANDONED;
            tx.put_change(ctx, sc).await?;
        }
        Ok(())
    }
}

/// subChanges lists the sub-changes of a change.
pub(crate) async fn sub_changes(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<Vec<Change>> {
    let all = tx.changes(ctx).await?;
    Ok(all.into_iter().filter(|c| &c.parent_id == id).collect())
}

/// openSubChanges are the sub-changes that still have to be applied or abandoned.
pub(crate) async fn open_sub_changes(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    id: &ChangeID,
) -> Result<Vec<Change>> {
    let mut subs = sub_changes(ctx, tx, id).await?;
    subs.retain(|c| c.status != CHANGE_APPLIED && c.status != CHANGE_ABANDONED);
    Ok(subs)
}
