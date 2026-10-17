//! Part of `apply.go` (G1): moving the head of a branch and numbering versions. The commit and the merge (`Apply`,
//! `CommitChange`, `IntegrateChange`, the applier, `writeVersion`) belong to a later slice (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{BRANCH_OPEN, Baseline, Branch, MAIN_BRANCH, NodeID, Version, namespace_of};

use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// advanceBranch moves the head of a branch to a baseline built on it (main is created on first use). The head
    /// moves only from the baseline the new one follows (ADR 0127): read under the lock of the branch, a head another
    /// transaction moved meanwhile is a conflict, never a head silently replaced.
    pub(crate) async fn advance_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        namespace: &str,
        name: &str,
        head: &Baseline,
    ) -> Result<()> {
        let namespace = namespace_of(namespace);
        tx.lock_branch(ctx, namespace, name).await?;
        let mut b = match tx.branch(ctx, namespace, name).await {
            Err(e) if e.is_not_found() && name == MAIN_BRANCH => Branch {
                name: name.to_string(),
                namespace: namespace.to_string(),
                status: BRANCH_OPEN.to_string(),
                created_at: self.now(),
                ..Default::default()
            },
            r => r?,
        };
        if b.head != head.parent_id {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "branch {name} moved to {} while baseline {} was built on {}: merge again",
                    b.head, head.id, head.parent_id
                ),
            ));
        }
        b.head = head.id.clone();
        tx.put_branch(ctx, b).await
    }
}

/// nextVersion returns the next version number of a node (numbered across branches).
pub(crate) async fn next_version(ctx: &Ctx, tx: &mut dyn Tx, id: &NodeID) -> Result<Version> {
    let vs = tx.versions(ctx, id).await?;
    Ok(vs.len() as Version + 1)
}

/// cpost is a version an accepted change impact produced (ADR 0024, Go's `changeimpact_apply.go`): the impact, its
/// post version and the version it was checked out from (None for a creation), what the node validators are given
/// (validator.go).
#[derive(Debug, Clone, Default)]
pub(crate) struct Cpost {
    pub(crate) cn: goap_domain::ChangeImpact,
    pub(crate) post: goap_domain::Node,
    pub(crate) pre: Option<goap_domain::Node>,
}
