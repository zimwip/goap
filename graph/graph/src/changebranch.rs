//! Part of `changebranch.go` (G1): the branch a change owns. Integrating a committed change (`integrateTx`,
//! `integrate`, `fastForward`) belongs to a later slice (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{
    BRANCH_OPEN, Branch, Change, ChangeID, INTENT_DERIVE, MAIN_BRANCH, branch_of,
    change_branch_origin,
};

use crate::merge::branch_of_tx;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// changeBranchName names the branch owned by a change.
pub(crate) fn change_branch_name(id: &ChangeID) -> String {
    let s = id.as_str();
    let s = if s.len() > 8 {
        s.get(..8).unwrap_or(s)
    } else {
        s
    };
    format!("change-{s}")
}

/// ownBranch returns the branch owned by the change, when it has one.
pub(crate) async fn own_branch(ctx: &Ctx, tx: &mut dyn Tx, c: &Change) -> Result<Option<Branch>> {
    if branch_of(&c.branch) == MAIN_BRANCH {
        return Ok(None);
    }
    let b = tx.branch(ctx, &c.namespace, &c.branch).await?;
    if b.origin == change_branch_origin(&c.id) {
        Ok(Some(b))
    } else {
        Ok(None)
    }
}

impl Graph {
    /// ensureOwnBranch gives a change the branch of its own that the versions it writes live on (ADR 0024), forked
    /// from its reference baseline like the one opened with the change. The items of a change are only applied when
    /// it is, so nothing is on the branch it acted on yet.
    pub(crate) async fn ensure_own_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        mut c: Change,
    ) -> Result<(Change, Branch)> {
        if !c.parent_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change {} is a sub-change without a branch of its own",
                    c.id
                ),
            ));
        }
        let parent = branch_of_tx(ctx, tx, &c.namespace, &c.branch).await?;
        if parent.status != BRANCH_OPEN {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("branch {} is {}", parent.name, parent.status),
            ));
        }
        let fork = tx.baseline(ctx, &c.baseline_id).await?;
        let own = Branch {
            name: change_branch_name(&c.id),
            namespace: c.namespace.clone(),
            parent: parent.name.clone(),
            fork_baseline: fork.id.clone(),
            head: fork.id,
            origin: change_branch_origin(&c.id),
            intent: INTENT_DERIVE,
            status: BRANCH_OPEN.to_string(),
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_branch(ctx, own.clone()).await?;
        c.branch = own.name.clone();
        tx.put_change(ctx, c.clone()).await?;
        Ok((c, own))
    }
}
