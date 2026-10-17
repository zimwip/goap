//! Port of `history.go`: the facets the graph folds from the log of a change (ADR 0109 §4).

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{Change, FACET_HISTORY, FacetValue, LogEntry};
use goap_graph::{Error, LogFacet, LogFacetFold, Result};
use goap_journal::{FoldWith, StepDependencies};

/// HistoryFold folds the history of a change (`goap_journal::History`) for its blackboard.
pub struct HistoryFold(pub FoldWith);

#[async_trait::async_trait]
impl LogFacetFold for HistoryFold {
    async fn fold(&self, ctx: &Ctx, c: &Change, entries: &[LogEntry]) -> Result<FacetValue> {
        let h = self.0.fold(ctx, c, entries).await.map_err(Error::other)?;
        Ok(Arc::new(h))
    }
}

/// LogFacets are the facets the graph folds from the log of a change for its blackboard: its history (ADR 0109 §4),
/// read by the conditions, the guards, the gates and the guardians the graph evaluates. deps gives the step
/// dependencies of a methodology (the registry in process); unset: no step relies on another, a success stands while
/// nothing it started from moved since.
pub fn log_facets(deps: Option<Arc<dyn StepDependencies>>) -> BTreeMap<String, LogFacet> {
    BTreeMap::from([(
        FACET_HISTORY.to_string(),
        LogFacet {
            types: goap_journal::history_types(),
            fold: Arc::new(HistoryFold(goap_journal::fold_with(deps))),
        },
    )])
}
