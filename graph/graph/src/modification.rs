//! What a change modifies (ADR 0102): a change impact is a modification when its draft
//! differs from the version it was checked out from (or creates a node). A candidate (an impact with no draft) or a
//! draft left as it was checked out is not: it cannot be accepted and is not committed, whoever works on the change.

use goap_ctx::Ctx;
use goap_domain::{ChangeID, ChangeImpactID, Draft, draft_changed};

use crate::draft::own_draft;
use crate::repo::Tx;
use crate::{Graph, Result};

impl Graph {
    /// draftModifies reports whether a draft is a modification of the version it was checked out from.
    pub(crate) async fn draft_modifies(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        d: Option<&Draft>,
    ) -> Result<bool> {
        let Some(d) = d else {
            return Ok(false);
        };
        let Some(b) = &d.base else {
            return Ok(true);
        };
        let base = self.stored_draft(ctx, tx, b).await?;
        Ok(draft_changed(Some(&base), d))
    }

    /// ImpactModifies reports whether a change impact of the main flow holds a modification of its node (ADR 0102).
    pub async fn impact_modifies(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
    ) -> Result<bool> {
        let mut tx = self.begin(ctx).await?;
        let rows = self.drafts(ctx, &mut tx, id).await?;
        let ok = self
            .draft_modifies(ctx, &mut tx, own_draft(&rows, impact, "").as_ref())
            .await?;
        tx.commit(ctx).await?;
        Ok(ok)
    }
}
