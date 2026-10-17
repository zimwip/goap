//! The cascade of the Activity of a sub-change.

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{Change, MAIN_BRANCH, NAMESPACE_METHODOLOGY, NodeID};
use goap_graphapi::{Error as GraphError, ErrorKind};

use crate::activitygoals::activity_of;
use crate::error::{Error, Result};
use crate::graphstore::{LINK_SUB_ACTIVITY, StoreGraph, baseline_with_suspects};
use crate::service::Service;

impl Service {
    /// SubChangeValidator resolves graph.Graph.sub_change_validator (architecture plan "Activity concept" cascade):
    /// the Activity a sub-change is scoped to must be its parent's or a descendant of it, following the sub_activity
    /// links of the methodology namespace. Unlike the unit and the project, an unset activity is not inherited. A
    /// registry with no graph behind its store (MemoryStore) validates nothing.
    pub async fn sub_change_validator(
        &self,
        ctx: &Ctx,
        parent: &Change,
        child: &Change,
    ) -> Result<()> {
        let (r, ancestor) = (activity_of(child), activity_of(parent));
        if r.is_empty() || ancestor.is_empty() || r == ancestor {
            return Ok(());
        }
        let Some(g) = self.store.graph() else {
            return Ok(());
        };
        if !activity_within(ctx, g.as_ref(), &r, &ancestor).await? {
            return Err(Error::Graph(GraphError::wrap(
                ErrorKind::Invalid,
                format!(
                    "activity {r} is not part of {ancestor}, the activity of the parent change"
                ),
            )));
        }
        Ok(())
    }
}

/// activityWithin reports whether the Activity with key r is ancestor or a descendant of it, walking up the
/// sub_activity links (parent -> child) of the head of the methodology namespace's main branch, suspect ones
/// included.
async fn activity_within(ctx: &Ctx, g: &dyn StoreGraph, r: &str, ancestor: &str) -> Result<bool> {
    let head = g
        .branch_head(ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await?;
    let (nodes, links) = baseline_with_suspects(g, ctx, &head.id).await?; // a step edited since keeps its parent
    let keys: HashMap<NodeID, String> = nodes.into_iter().map(|n| (n.id, n.key)).collect();
    let mut parent_of: HashMap<String, String> = HashMap::new();
    for l in links {
        if l.r#type == LINK_SUB_ACTIVITY {
            parent_of.insert(
                keys.get(&l.to.id).cloned().unwrap_or_default(),
                keys.get(&l.from.id).cloned().unwrap_or_default(),
            );
        }
    }
    let mut seen: HashSet<String> = HashSet::new();
    let mut cur = r.to_string();
    while !cur.is_empty() && !seen.contains(&cur) {
        if cur == ancestor {
            return Ok(true);
        }
        seen.insert(cur.clone());
        cur = parent_of.get(&cur).cloned().unwrap_or_default();
    }
    Ok(false)
}
