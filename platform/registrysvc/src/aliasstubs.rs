//! The model aliases a published methodology needs, proposed as stubs (ADR 0018, 0021).

use std::collections::HashSet;

use goap_ctx::Ctx;
use goap_domain::{CHANGE_ABANDONED, CHANGE_APPLIED, MAIN_BRANCH};
use goap_graphapi::{Error as GraphError, NewChange, NodeCreate};
use goap_llmcfg::{NAMESPACE_PLATFORM, NODE_TYPE_ALIAS, alias_key};
use goap_methodology::Methodology;
use serde_json::{Map, Value};

use crate::graphstore::{StoreGraph, root_project};
use crate::service::Service;

type GResult<T> = std::result::Result<T, GraphError>;

impl Service {
    /// ensureAliasStubs finds every LLM alias an agent or action of m references and, for any that names no
    /// platform@LlmAlias node yet, opens a pending ChangeImpact on the platform namespace proposing it as a stub (no
    /// target yet), so an administrator sees it in the change-review queue and sets the target of the draft before
    /// accepting it: the domain refuses an alias with none. Best-effort: an error here is never returned, it must
    /// never block a publish.
    pub(crate) async fn ensure_alias_stubs(&self, ctx: &Ctx, m: &Methodology) {
        // check-then-create must not interleave: methodologies are seeded and published concurrently
        let _guard = self.alias_mu.lock().await;
        let Some(g) = self.store.graph() else {
            return; // no graph behind this registry (a MemoryStore): nothing to stub
        };
        let aliases = referenced_aliases(m);
        if aliases.is_empty() {
            return;
        }
        let Ok(missing) = missing_aliases(ctx, g.as_ref(), &aliases).await else {
            return;
        };
        let mut stubs = Vec::new();
        for alias in missing {
            match has_pending_stub(ctx, g.as_ref(), &alias).await {
                Ok(false) => stubs.push(alias),
                _ => continue,
            }
        }
        if !stubs.is_empty()
            && let Err(err) = create_alias_stubs(ctx, g.as_ref(), &stubs, &m.name).await
        {
            tracing::warn!(methodology = %m.name, aliases = ?stubs, %err, "registry: could not propose the alias stubs");
        }
    }
}

/// referencedAliases collects every non-empty Agent.model / Action.model of m, deduplicated.
pub(crate) fn referenced_aliases(m: &Methodology) -> Vec<String> {
    let mut seen = HashSet::new();
    let mut out = Vec::new();
    let models = m
        .agents
        .iter()
        .map(|a| &a.model)
        .chain(m.actions.iter().map(|a| &a.model));
    for alias in models {
        if !alias.is_empty() && seen.insert(alias.clone()) {
            out.push(alias.clone());
        }
    }
    out
}

/// missingAliases returns which of aliases has no platform@LlmAlias node at the head of platform/main (an
/// existing-but-broken alias, present with an empty target, still counts as existing: it is not stubbed again).
async fn missing_aliases(
    ctx: &Ctx,
    g: &dyn StoreGraph,
    aliases: &[String],
) -> GResult<Vec<String>> {
    let head = match g.branch_head(ctx, NAMESPACE_PLATFORM, MAIN_BRANCH).await {
        Err(e) if e.is_not_found() => return Ok(aliases.to_vec()), // no platform graph yet: every alias is missing
        Err(e) => return Err(e),
        Ok(h) => h,
    };
    let (nodes, _) = g.baseline_graph(ctx, &head.id).await?;
    let have: HashSet<String> = nodes
        .into_iter()
        .filter(|n| n.namespace == NAMESPACE_PLATFORM && n.r#type == NODE_TYPE_ALIAS)
        .map(|n| n.key)
        .collect();
    Ok(aliases
        .iter()
        .filter(|a| !have.contains(&alias_key(a)))
        .cloned()
        .collect())
}

/// hasPendingStub reports whether an open (not applied / abandoned) platform-namespace change already holds the
/// LlmAlias node of alias, whatever the state of its review: no duplicate stub when a methodology referencing it is
/// republished before an administrator acts. (Change.nodes is the impact projection, filled by Graph.Changes.)
async fn has_pending_stub(ctx: &Ctx, g: &dyn StoreGraph, alias: &str) -> GResult<bool> {
    let changes = g.changes(ctx).await?;
    let key = alias_key(alias);
    Ok(changes.iter().any(|c| {
        c.namespace == NAMESPACE_PLATFORM
            && c.status != CHANGE_APPLIED
            && c.status != CHANGE_ABANDONED
            && c.nodes
                .iter()
                .any(|cn| cn.key == key && cn.r#type == NODE_TYPE_ALIAS)
    }))
}

/// createAliasStubs opens one platform-namespace change proposing a stub platform@LlmAlias node (name only, no
/// target) per alias a methodology needs, as pending change impacts: the administrator reviews them together. It
/// never commits (which auto-accepts) nor reviews: the change stays open and visible in the review queue until an
/// administrator reviews it.
async fn create_alias_stubs(
    ctx: &Ctx,
    g: &dyn StoreGraph,
    aliases: &[String],
    methodology_name: &str,
) -> GResult<()> {
    let head = g.branch_head(ctx, NAMESPACE_PLATFORM, MAIN_BRANCH).await?;
    let root = root_project(ctx, g).await?;
    let list = aliases.join(", ");
    let c = g
        .create_change(
            ctx,
            NewChange {
                namespace: NAMESPACE_PLATFORM.to_string(),
                project_id: root,
                title: format!("Model aliases needed by {methodology_name} ({list})"),
                intent: format!(
                    "methodology {methodology_name} references the model aliases {list}: each needs a target (provider/model) before this change can be accepted"
                ),
                baseline_id: head.id,
                own_branch: true,
                ..NewChange::default()
            },
        )
        .await?;
    for alias in aliases {
        // a draft, its review proposed (ADR 0076, 0079): the change stays open
        let mut props = Map::new();
        props.insert("alias".into(), Value::String(alias.clone()));
        g.impact_node_create(
            ctx,
            &c.id,
            NodeCreate {
                key: alias_key(alias),
                r#type: NODE_TYPE_ALIAS.to_string(),
                properties: props,
                rationale: format!(
                    "alias {alias} needs a target provider/model before this change can be accepted (referenced by methodology {methodology_name})"
                ),
                produced_by: "methodology-load".into(),
                ..NodeCreate::default()
            },
        )
        .await?;
    }
    Ok(())
}
