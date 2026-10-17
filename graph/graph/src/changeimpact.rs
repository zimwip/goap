//! Part of `changeimpact.go` (G1): the start of a write on a change. The operations on the change impacts
//! (ProposeImpact, the reviews, ...) belong to a later slice (see PORTING.md); `Graph::change_tx` is in guard.rs.

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_APPLIED, CHANGE_COMMITTED, Change, ChangeID, ChangeImpactID,
};

use crate::repo::Tx;
use crate::{Error, ErrorKind, Result};

/// changeOpen loads a change and checks that it still accepts change impacts. It is the start of a write: it takes the
/// lock of the change's tree first (ADR 0127).
pub(crate) async fn change_open(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<Change> {
    tx.lock_change(ctx, id).await?;
    let c = tx.change(ctx, id).await?;
    if c.status == CHANGE_APPLIED || c.status == CHANGE_ABANDONED || c.status == CHANGE_COMMITTED {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("change {id} is {}", c.status),
        ));
    }
    Ok(c)
}

/// findChangeImpact returns the position of a change impact in its change.
pub(crate) fn find_change_impact(c: &Change, id: &ChangeImpactID) -> Result<usize> {
    c.nodes.iter().position(|cn| &cn.id == id).ok_or_else(|| {
        Error::wrap(
            ErrorKind::NotFound,
            format!("change impact {id} of change {}", c.id),
        )
    })
}
