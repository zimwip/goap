//! Asking the OperationAuthorizer (ADR 0103) before an operation of the catalogue
//! (`goap_operations`). The authorizer itself is the [`crate::OperationAuthorizer`] trait of graph.rs.
//!
//! Every method of the graph a person performs on a change carries, in its doc, `/// goap:operation <code>
//! <doer|reviewer>` and calls `self.allow(ctx, id, operations::CONST).await?` (with `use goap_operations as
//! operations;`) before its transaction; goap-opgen reads these annotations to write the catalogue.

use goap_ctx::Ctx;
use goap_domain::ChangeID;

use crate::{Graph, Result};

impl Graph {
    /// allow asks the OperationAuthorizer whether the caller may perform op on the change, before any transaction.
    /// Every method of the catalogue calls it first (goap-opgen checks it); a change that does not exist is left to
    /// the operation to report.
    pub(crate) async fn allow(&self, ctx: &Ctx, id: &ChangeID, op: &str) -> Result<()> {
        let Some(a) = &self.operation_authorizer else {
            return Ok(());
        };
        let Ok(c) = self.change(ctx, id).await else {
            return Ok(());
        };
        a.authorize(ctx, &c, op).await
    }
}
