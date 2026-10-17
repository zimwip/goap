//! The history of a change (ADR 0109 §4, Go's `history.go`).

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{Blackboard, ChangeID, FACET_HISTORY, LogFilter};
use goap_journal::{StepDependencies, StepDeps};

use crate::engine::Engine;
use crate::error::Result;
use crate::ports::MethodologyPort;

/// MethodologyDeps are the step dependencies of the methodologies as the engine compiles them
/// (journal::StepDependencies).
pub struct MethodologyDeps(pub Arc<dyn MethodologyPort>);

#[async_trait]
impl StepDependencies for MethodologyDeps {
    async fn step_dependencies(&self, ctx: &Ctx, methodology: &str) -> Option<StepDeps> {
        self.0
            .methodology(ctx, methodology)
            .await
            .ok()
            .map(|m| m.step_dependencies())
    }
}

impl Engine {
    /// with_history gives the blackboard of a change its history when the graph did not (a graph reached over RPC
    /// carries no log facet).
    pub async fn with_history(&self, ctx: &Ctx, bb: Blackboard) -> Result<Blackboard> {
        if bb.change.id.is_empty() || bb.facets.contains_key(FACET_HISTORY) {
            return Ok(bb);
        }
        let (entries, _) = self
            .graph
            .change_log(
                ctx,
                LogFilter {
                    change: bb.change.id.clone(),
                    types: goap_journal::history_types(),
                    ..Default::default()
                },
            )
            .await?;
        let deps: Arc<dyn StepDependencies> = Arc::new(MethodologyDeps(self.methodologies.clone()));
        let h = goap_journal::fold_with(Some(deps))
            .fold(ctx, &bb.change, &entries)
            .await?;
        Ok(bb.with_facet(FACET_HISTORY, h))
    }

    /// step_dependencies are the step dependencies of a methodology as the engine compiles it.
    pub async fn step_dependencies(&self, ctx: &Ctx, methodology: &str) -> Option<StepDeps> {
        MethodologyDeps(self.methodologies.clone())
            .step_dependencies(ctx, methodology)
            .await
    }

    /// blackboard is the blackboard of a change as a flow sees it, with its history.
    pub(crate) async fn blackboard(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Blackboard> {
        let bb = self.graph.blackboard_in(ctx, id, flow).await?;
        self.with_history(ctx, bb).await
    }
}
