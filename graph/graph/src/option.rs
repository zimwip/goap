//! Part of `option.go` (G1): the facets of a blackboard (ADR 0066, 0109). The options themselves (OpenOption,
//! SelectOption, the flow operations, the blackboard reads) belong to a later slice (see PORTING.md).

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    Blackboard, Change, ChangeID, FACET_ACTIVE_OPTION, FACET_DECISION_POINTS, FACET_OPTIONS,
    FacetValue, Flow, LogEntry, LogFilter,
};

use crate::repo::Tx;
use crate::{Graph, Result};

/// BlackboardFacet computes a facet of the blackboard of a change (Blackboard::facets) from the change as stored,
/// replayed at now.
pub type BlackboardFacet = Arc<dyn Fn(&Change, DateTime<Utc>) -> FacetValue + Send + Sync>;

/// The fold of a [`LogFacet`]: the facet from the entries of the log of a change.
#[async_trait]
pub trait LogFacetFold: Send + Sync {
    async fn fold(&self, ctx: &Ctx, c: &Change, entries: &[LogEntry]) -> Result<FacetValue>;
}

/// LogFacet is a facet of the blackboard of a change folded from the entries of its log of the given types (a stream
/// "journal." or an exact type); the graph reads them, the use case that plugs it knows what they hold.
#[derive(Clone)]
pub struct LogFacet {
    pub types: Vec<String>,
    pub fold: Arc<dyn LogFacetFold>,
}

impl Graph {
    /// logEntries reads the entries of the log of a change each log facet folds.
    pub(crate) async fn log_entries(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<BTreeMap<String, Vec<LogEntry>>> {
        let mut out = BTreeMap::new();
        for (name, f) in &self.log_facets {
            let es = tx
                .log(
                    ctx,
                    &LogFilter {
                        change: id.clone(),
                        types: f.types.clone(),
                        ..Default::default()
                    },
                )
                .await?;
            out.insert(name.clone(), es);
        }
        Ok(out)
    }

    /// withLogFacets folds the log facets of the blackboard from the entries read with it (outside any transaction).
    pub(crate) async fn with_log_facets(
        &self,
        ctx: &Ctx,
        mut bb: Blackboard,
        entries: &BTreeMap<String, Vec<LogEntry>>,
    ) -> Result<Blackboard> {
        for (name, es) in entries {
            let Some(f) = self.log_facets.get(name) else {
                continue;
            };
            let v = f.fold.fold(ctx, &bb.change, es).await?;
            bb.facets.insert(name.clone(), v);
        }
        Ok(bb)
    }

    /// facets are the facets of the blackboard of a change: the built-in ones (the options and the decision points
    /// being the graph's own mechanisms, ADR 0009, 0032: what a view of a flow does not carry), then Graph::facets
    /// (which may replace them).
    pub(crate) fn facets_of(&self, c: &Change, now: DateTime<Utc>) -> HashMap<String, FacetValue> {
        let mut out: HashMap<String, FacetValue> = HashMap::new();
        out.insert(FACET_OPTIONS.into(), Arc::new(c.options()));
        out.insert(FACET_ACTIVE_OPTION.into(), Arc::new(c.active_option()));
        out.insert(
            FACET_DECISION_POINTS.into(),
            Arc::new(c.decision_points_at(now, self.decision_policy.as_deref())),
        );
        for (name, f) in &self.facets {
            out.insert(name.clone(), f(c, now));
        }
        out
    }
}

/// flowIDsOf lists the ids of flows.
pub(crate) fn flow_ids_of(fs: &[Flow]) -> Vec<String> {
    fs.iter().map(|f| f.id.clone()).collect()
}
