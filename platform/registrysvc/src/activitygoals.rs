//! Port of `activitygoals.go`: the Activity a change is scoped to, and the guardian of its merge (ADR 0066, 0102).

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{Blackboard, Change, methodology_ref};
use goap_methodology::Compiled;

use crate::error::{Error, Result};
use crate::service::Service;

/// DataActivity is the key of Change.data holding the Activity a change is scoped to (architecture plan "Activity
/// concept"): the node key of a methodology@Process/Step/Method/MethodStep ("MV:<name>@<version>/<kind>/<item>"),
/// the activity whose goal condition the change must satisfy to merge. The graph never reads it: Service::merge (the
/// guardian of a merge, graph.ChangeLifecycles) and Service::sub_change_validator do. Absent: no activity-relative
/// gating beyond a node type's own lifecycle.
pub const DATA_ACTIVITY: &str = "activityRef";

/// ActivityOf is the Activity a change is scoped to ("" when it has none).
pub fn activity_of(c: &Change) -> String {
    c.data
        .get(DATA_ACTIVITY)
        .and_then(|v| v.as_str())
        .unwrap_or_default()
        .to_string()
}

impl Service {
    /// Merge implements graph.ChangeLifecycles (ADR 0066, 0102): a change scoped to an Activity (DATA_ACTIVITY) is
    /// merged when that activity's own goal condition holds, instead of the node-type lifecycle's landable-state
    /// floor; any other change is not decided. It returns (decided, ok).
    pub async fn merge(&self, ctx: &Ctx, c: &Change, bb: &Blackboard) -> Result<(bool, bool)> {
        let r = activity_of(c);
        if r.is_empty() {
            return Ok((false, false));
        }
        let ok = self.activity_goals_met(ctx, &r, bb).await?;
        Ok((true, ok))
    }

    /// ActivityGoalsMet: given an Activity's node key, it loads and compiles that activity's own methodology version,
    /// then evaluates the activity's own goal condition against bb (built by the graph from the change's own
    /// impacts, ADR 0024).
    pub async fn activity_goals_met(
        &self,
        ctx: &Ctx,
        activity_ref: &str,
        bb: &Blackboard,
    ) -> Result<bool> {
        let Some((name, version, kind, item)) = parse_activity_ref(activity_ref) else {
            return Err(Error::not_found(format!(
                "{activity_ref}: not an activity key"
            )));
        };
        let c = self
            .methodology(ctx, &methodology_ref(name, version))
            .await?;
        let cond = activity_condition(&c, kind, item)
            .map_err(|e| Error::Other(format!("{activity_ref}: {e}")))?;
        let state = c.conditions.evaluate(bb).state;
        Ok(cond
            .iter()
            .all(|(k, want)| state.get(k).copied().unwrap_or(false) == *want))
    }
}

/// activityCondition is the goal condition of a compiled Activity: a process's or a steps-composed method's own
/// generated goal, a plain-agent method's named goal, or a step's/method-step's own exit criteria - the same
/// condition map shape (CEL condition names to required values) in every case.
fn activity_condition(
    c: &Compiled,
    kind: &str,
    item: &str,
) -> std::result::Result<BTreeMap<String, bool>, String> {
    match kind {
        "process" => c
            .goal(item)
            .map(|g| g.pre)
            .ok_or_else(|| format!("process {item:?}: no such goal")),
        "method" => c
            .goal(&c.method_goal(item))
            .map(|g| g.pre)
            .ok_or_else(|| format!("method {item:?}: no goal")),
        "step" | "methodStep" => c
            .step_criteria(item)
            .map(|(_, exit)| exit)
            .ok_or_else(|| format!("step {item:?}: unknown")),
        _ => Err(format!("unknown activity kind {kind:?}")),
    }
}

/// parseActivityRef splits an Activity node key ("MV:<name>@<version>/<kind>/<item>", built by versionEdits /
/// stepChildren) back into its methodology name, version, element kind and item name or step path.
pub(crate) fn parse_activity_ref(r: &str) -> Option<(&str, &str, &str, &str)> {
    let rest = r.strip_prefix("MV:")?;
    let mut parts = rest.splitn(3, '/');
    let (head, kind, item) = (parts.next()?, parts.next()?, parts.next()?);
    let (name, version) = head.split_once('@')?;
    Some((name, version, kind, item))
}
