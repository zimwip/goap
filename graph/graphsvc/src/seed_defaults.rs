//! The seeds of the platform, each one change on main (ADR 0056, 0071).

use std::collections::BTreeMap;

use goap_access as access;
use goap_adapter as adapter;
use goap_authz::{DEFAULT_POLICIES, FLOOR_POLICIES, Policy};
use goap_criticality::{Level, Policy as CriticalityPolicy};
use goap_ctx::Ctx;
use goap_domain::{
    MAIN_BRANCH, NAMESPACE_PLATFORM, Node, NodeRef, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT,
    TYPE_ADAPTER, TYPE_ADAPTER_DEF,
};
use goap_graph::{Commit, Graph, LinkEdit, NodeEdit, Result};
use goap_llmcfg as llmcfg;
use goap_mcp as mcp;
use goap_mcpbuiltin as mcpbuiltin;
use serde_json::{Map, Value};

use crate::personal::{new_user_unit, system};

/// SeedChange commits node edits on main as one change of a namespace, so that the head of main moves: the first
/// change of a namespace starts from the empty state (ADR 0056).
pub async fn seed_change(
    ctx: &Ctx,
    g: &Graph,
    namespace: &str,
    title: &str,
    edits: Vec<NodeEdit>,
) -> Result<()> {
    // a seed acts as the graph service itself
    let ctx = if goap_authz::principal_from(ctx).anonymous() {
        system(ctx)
    } else {
        ctx.clone()
    };
    let out = Box::pin(g.commit(
        &ctx,
        Commit {
            namespace: namespace.to_string(),
            title: title.to_string(),
            intent: title.to_string(),
            by: "graphsvc.seed".into(),
            baseline_name: title.to_string(),
            edits,
            // a seed acts in the root project (ADR 0091)
            project_id: g.structure(STRUCTURE_PROJECT).root,
            ..Default::default()
        },
    ))
    .await;
    match out {
        Ok(_) => Ok(()),
        // the graph holds what the seed states already
        Err(e) if e.is_nothing_to_commit() => Ok(()),
        Err(e) => Err(e),
    }
}

/// SeedNode is the edit creating a node of a seed.
pub fn seed_node(key: &str, typ: &str, props: Map<String, Value>) -> NodeEdit {
    NodeEdit {
        key: key.to_string(),
        r#type: typ.to_string(),
        props,
        rationale: format!("Seed {key}"),
        ..Default::default()
    }
}

/// linkTo links a created node to an existing node of the graph.
pub(crate) fn link_to(mut e: NodeEdit, typ: &str, to: NodeRef) -> NodeEdit {
    e.links.push(LinkEdit {
        r#type: typ.to_string(),
        to: Some(to),
        ..Default::default()
    });
    e
}

/// SeedUnit creates a unit of the organisation, a child of parent (the root unit when parent is empty: every unit but
/// the root itself needs one, ADR 0040).
pub async fn seed_unit(
    ctx: &Ctx,
    g: &Graph,
    key: &str,
    name: &str,
    kind: &str,
    parent: &str,
) -> Result<()> {
    let mut props = Map::new();
    props.insert("name".into(), name.into());
    props.insert("kind".into(), kind.into());
    let unit = seed_node(key, access::NODE_TYPE_ORG_UNIT, props);
    let parent = if parent.is_empty() {
        g.structure(STRUCTURE_ORGANISATION).root
    } else {
        parent.to_string()
    };
    let p = g
        .node_by_key(ctx, access::NAMESPACE_ORGANISATION, &parent)
        .await?;
    let unit = link_to(unit, access::LINK_PART_OF, p.r#ref());
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("Unit {key}"),
        vec![unit],
    )
    .await
}

/// SeedAdapter creates the Adapter node of a unit, owned by it (ADR 0054: the owner of its versions).
pub async fn seed_adapter(ctx: &Ctx, g: &Graph, a: &adapter::Instance) -> Result<()> {
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("Adapter {} of {}", a.mcp, a.unit),
        vec![owned_by(
            seed_node(&adapter::key(&a.unit, &a.mcp), TYPE_ADAPTER, a.props()),
            &a.unit,
        )],
    )
    .await
}

/// SeedCriticalityPolicy creates what a criticality level requires of the changes of a unit (ADR 0075 §3), owned by
/// it.
pub async fn seed_criticality_policy(
    ctx: &Ctx,
    g: &Graph,
    unit: &str,
    l: &Level,
    p: &CriticalityPolicy,
) -> Result<()> {
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("Criticality policy {l} of {unit}"),
        vec![owned_by(
            seed_node(
                &access::criticality_policy_key(unit, l),
                access::NODE_TYPE_CRITICALITY_POLICY,
                access::criticality_props(l, p),
            ),
            unit,
        )],
    )
    .await
}

/// ownedBy makes the unit with key unit the owner of a node an edit creates or modifies (ADR 0054).
fn owned_by(mut e: NodeEdit, unit: &str) -> NodeEdit {
    e.owner = unit.to_string();
    e
}

/// SeedAdapterDef creates the AdapterDef node of an adapter definition in the platform namespace.
pub async fn seed_adapter_def(ctx: &Ctx, g: &Graph, d: &adapter::Def) -> Result<()> {
    seed_change(
        ctx,
        g,
        NAMESPACE_PLATFORM,
        &format!("Adapter {}", d.name),
        vec![seed_node(
            &adapter::def_key(&d.name),
            TYPE_ADAPTER_DEF,
            d.props(),
        )],
    )
    .await
}

/// SeedAccess makes sure the default policies exist as Policy nodes of the organisation namespace. It is idempotent:
/// once the floor policy exists nothing is touched, so that edited or deleted policies stay so.
pub async fn seed_access(ctx: &Ctx, g: &Graph) -> Result<bool> {
    match g
        .node_by_key(
            ctx,
            access::NAMESPACE_ORGANISATION,
            &access::policy_key(&FLOOR_POLICIES[0]),
        )
        .await
    {
        Ok(_) => return Ok(false),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    let items = DEFAULT_POLICIES
        .iter()
        .map(|p| {
            seed_node(
                &access::policy_key(p),
                access::NODE_TYPE_POLICY,
                access::policy_props(p),
            )
        })
        .collect();
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        "Default policies",
        items,
    )
    .await?;
    Ok(true)
}

/// SeedUser creates the User node of a subject, member of a unit (new_user_unit when u.unit is empty: member_of is
/// exactly one link, ADR 0040, never left unset).
pub async fn seed_user(ctx: &Ctx, g: &Graph, u: &access::User) -> Result<()> {
    let mut user = seed_node(
        &access::user_key(&u.subject),
        access::NODE_TYPE_USER,
        u.props(),
    );
    // land it active (ADR 0048's user lifecycle): a User seeded this way (tools, tests) must be usable right away, the
    // same as one created by createUser's sign-in flow — left "proposed" it would silently never count toward the
    // admin floor until some unrelated later commit happened to touch User/Assignment.
    user.state = "active".into();
    let unit = if u.unit.is_empty() {
        new_user_unit(ctx, g).await?
    } else {
        g.node_by_key(ctx, access::NAMESPACE_ORGANISATION, &u.unit)
            .await?
    };
    let user = link_to(user, access::LINK_MEMBER_OF, unit.r#ref());
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("User {}", u.subject),
        vec![user],
    )
    .await
}

/// SeedPolicy creates a Policy node.
pub async fn seed_policy(ctx: &Ctx, g: &Graph, p: &Policy) -> Result<()> {
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        &format!("Policy {}/{}", p.resource, p.action),
        vec![seed_node(
            &access::policy_key(p),
            access::NODE_TYPE_POLICY,
            access::policy_props(p),
        )],
    )
    .await
}

/// The nodes of the head of main of a namespace (none while the namespace holds nothing).
async fn head_nodes(ctx: &Ctx, g: &Graph, namespace: &str) -> Result<Vec<Node>> {
    match g.branch_head(ctx, namespace, MAIN_BRANCH).await {
        Ok(head) if !head.id.is_empty() => Ok(g.baseline_graph(ctx, &head.id).await?.0),
        Ok(_) => Ok(Vec::new()),
        Err(e) if e.is_not_found() => Ok(Vec::new()),
        Err(e) => Err(e),
    }
}

/// SeedModels creates the model gateway configuration (providers, models, aliases; nodes of the platform namespace)
/// when the graph holds no provider yet, so that providers or models deleted on purpose stay so. It reports whether it
/// seeded.
pub async fn seed_models(
    ctx: &Ctx,
    g: &Graph,
    providers: &[llmcfg::Provider],
    models: &[llmcfg::Model],
    aliases: &[llmcfg::Alias],
) -> Result<bool> {
    let nodes = head_nodes(ctx, g, llmcfg::NAMESPACE_PLATFORM).await?;
    if nodes.iter().any(|n| {
        n.namespace == llmcfg::NAMESPACE_PLATFORM && n.r#type == llmcfg::NODE_TYPE_PROVIDER
    }) {
        return Ok(false);
    }
    let mut items = Vec::new();
    for p in providers {
        items.push(seed_node(
            &llmcfg::provider_key(&p.name),
            llmcfg::NODE_TYPE_PROVIDER,
            p.props(),
        ));
    }
    for m in models {
        items.push(seed_node(&m.key(), llmcfg::NODE_TYPE_MODEL, m.props()));
    }
    for a in aliases {
        items.push(seed_node(
            &llmcfg::alias_key(&a.alias),
            llmcfg::NODE_TYPE_ALIAS,
            a.props(),
        ));
    }
    if items.is_empty() {
        return Ok(false);
    }
    seed_change(
        ctx,
        g,
        llmcfg::NAMESPACE_PLATFORM,
        "Model gateway configuration",
        items,
    )
    .await?;
    Ok(true)
}

/// SeedProtectedAliases makes sure, at every start, that the protected aliases of the platform exist (ADR 0084:
/// llmcfg::protected_aliases): seed_models only runs on a graph holding no provider, so a graph that predates them gets
/// them here. A missing one is created targeting what the default alias targets (the helper: what the fast alias
/// targets when it exists), or nothing while no model is configured, in which case it resolves to nothing and is not
/// available; a node of that name without the protected flag, or retired, is flagged and restored. One that exists is
/// never retargeted: the target belongs to the administrators. It reports whether it wrote anything.
pub async fn seed_protected_aliases(ctx: &Ctx, g: &Graph) -> Result<bool> {
    let mut current: BTreeMap<String, Node> = BTreeMap::new();
    for n in head_nodes(ctx, g, llmcfg::NAMESPACE_PLATFORM).await? {
        if n.namespace == llmcfg::NAMESPACE_PLATFORM
            && n.r#type == llmcfg::NODE_TYPE_ALIAS
            && !n.deleted
        {
            current.insert(n.key.clone(), n);
        }
    }
    let target_of = |alias: &str| -> String {
        let Some(n) = current.get(&llmcfg::alias_key(alias)) else {
            return String::new();
        };
        if n.state == llmcfg::STATE_RETIRED {
            return String::new();
        }
        llmcfg::alias_from_props(&n.properties)
            .map(|a| a.target)
            .unwrap_or_default()
    };
    let mut edits = Vec::new();
    for name in llmcfg::protected_aliases() {
        let key = llmcfg::alias_key(name);
        let Some(n) = current.get(&key) else {
            let mut target = target_of("default");
            if name == llmcfg::HELPER_ALIAS {
                let fast = target_of("fast");
                if !fast.is_empty() {
                    target = fast;
                }
            }
            edits.push(seed_node(
                &key,
                llmcfg::NODE_TYPE_ALIAS,
                llmcfg::Alias {
                    alias: name.to_string(),
                    target,
                    protected: true,
                }
                .props(),
            ));
            continue;
        };
        if let Ok(a) = llmcfg::alias_from_props(&n.properties)
            && a.protected
            && n.state != llmcfg::STATE_RETIRED
        {
            continue;
        }
        let mut props = Map::new();
        props.insert("protected".into(), true.into());
        let mut e = NodeEdit {
            pre: Some(n.r#ref()),
            props,
            rationale: format!("Alias {name} is protected"),
            ..Default::default()
        };
        if n.state == llmcfg::STATE_RETIRED {
            e.state = "active".into();
        }
        edits.push(e);
    }
    if edits.is_empty() {
        return Ok(false);
    }
    seed_change(
        ctx,
        g,
        llmcfg::NAMESPACE_PLATFORM,
        "Protected model aliases",
        edits,
    )
    .await?;
    Ok(true)
}

/// SeedBehaviors gives every install the built-in example behaviour of the gateway (ADR 0093:
/// llmcfg::terse_behavior, enabled): created when no node of its key exists, never touched afterwards, so an
/// administrator who edited, disabled or retired it keeps it so. It reports whether it wrote anything.
pub async fn seed_behaviors(ctx: &Ctx, g: &Graph) -> Result<bool> {
    let b = llmcfg::terse_behavior();
    let key = llmcfg::behavior_key(&b.name);
    match g.node_by_key(ctx, llmcfg::NAMESPACE_PLATFORM, &key).await {
        Ok(_) => return Ok(false),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    seed_change(
        ctx,
        g,
        llmcfg::NAMESPACE_PLATFORM,
        "Built-in LLM behaviours",
        vec![seed_node(&key, llmcfg::NODE_TYPE_BEHAVIOR, b.props())],
    )
    .await?;
    Ok(true)
}

/// SeedBuiltins makes sure, at every start, that the built-in MCPs and their adapter definitions exist and match the
/// code (ADR 0028): like the built-in domains they ship with the platform. The first time a built-in MCP is seeded the
/// default organisation gets an instance of its adapter, so that every unit can use it; the instance is never
/// recreated afterwards, so that a unit restricting or an administrator removing it stays so. It reports whether it
/// wrote anything; Graph::bootstrap runs first (boot).
pub async fn seed_builtins(ctx: &Ctx, g: &Graph) -> Result<bool> {
    let mut current: BTreeMap<String, Node> = BTreeMap::new();
    for n in head_nodes(ctx, g, NAMESPACE_PLATFORM).await? {
        if !n.deleted {
            current.insert(n.key.clone(), n);
        }
    }
    let mut edits = Vec::new();
    let mut fresh = Vec::new();
    let mut sync = |key: &str, typ: &str, props: Map<String, Value>| match current.get(key) {
        None => edits.push(seed_node(key, typ, props)),
        Some(n) if !same_props(&n.properties, &props) => {
            let mut set = props.clone();
            for k in n.properties.keys() {
                if !props.contains_key(k) {
                    set.insert(k.clone(), Value::Null); // properties are merged: clear the ones the code dropped
                }
            }
            edits.push(NodeEdit {
                pre: Some(n.r#ref()),
                props: set,
                rationale: format!("Built-in {key} follows the platform"),
                ..Default::default()
            });
        }
        Some(_) => {}
    };
    let defs = mcpbuiltin::adapter_defs();
    for (i, d) in mcpbuiltin::defs().iter().enumerate() {
        if !current.contains_key(&mcp::mcp_key(&d.name)) {
            fresh.push(d.name.clone());
        }
        sync(&mcp::mcp_key(&d.name), mcp::NODE_TYPE_MCP, d.props());
        sync(
            &adapter::def_key(&defs[i].name),
            TYPE_ADAPTER_DEF,
            defs[i].props(),
        );
    }
    // the workspace MCP is defined everywhere and held by no unit by default (ADR 0118)
    let ws = mcpbuiltin::workspace_def();
    sync(&mcp::mcp_key(&ws.name), mcp::NODE_TYPE_MCP, ws.props());
    for d in mcpbuiltin::workspace_adapter_defs() {
        sync(&adapter::def_key(&d.name), TYPE_ADAPTER_DEF, d.props());
    }
    // so is the Claude coding agent (ADR 0123)
    let ca = mcpbuiltin::claude_agent_def();
    sync(&mcp::mcp_key(&ca.name), mcp::NODE_TYPE_MCP, ca.props());
    sync(
        &adapter::def_key(mcpbuiltin::CLAUDE_AGENT),
        TYPE_ADAPTER_DEF,
        mcpbuiltin::claude_agent_adapter_def().props(),
    );
    for r in access::builtin_roles() {
        sync(
            &access::role_key(&r.name),
            access::NODE_TYPE_ROLE,
            r.props(),
        );
    }
    let wrote = !edits.is_empty();
    if wrote {
        seed_change(ctx, g, NAMESPACE_PLATFORM, "Built-in MCPs", edits).await?;
    }
    if fresh.is_empty() {
        return Ok(wrote);
    }
    let mut instances = Vec::new();
    let root = g.structure(STRUCTURE_ORGANISATION).root;
    for name in &fresh {
        let a = mcpbuiltin::adapter(&root, name);
        let key = adapter::key(&a.unit, &a.mcp);
        match g
            .node_by_key(ctx, access::NAMESPACE_ORGANISATION, &key)
            .await
        {
            Ok(_) => continue,
            Err(e) if !e.is_not_found() => return Err(e),
            Err(_) => {}
        }
        instances.push(owned_by(seed_node(&key, TYPE_ADAPTER, a.props()), &a.unit));
    }
    if !instances.is_empty() {
        seed_change(
            ctx,
            g,
            access::NAMESPACE_ORGANISATION,
            "Built-in adapters of the default organisation",
            instances,
        )
        .await?;
    }
    Ok(true)
}

/// sameProps reports whether the properties of a node hold the wanted ones (compared as JSON).
fn same_props(have: &Map<String, Value>, want: &Map<String, Value>) -> bool {
    serde_json::to_string(have).ok() == serde_json::to_string(want).ok()
}
