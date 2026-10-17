//! Port of `graph/graph/graphtest` (see docs/rust-port.md).
//!
//! Lands the fixtures of tests through the operations every change uses (ADR 0076, 0079): a node is created in a
//! change of its own, moved to its state on its draft, accepted (as the tester: a fixture is a bulk system path, not
//! an edit) and applied, which writes its version.
//!
//! API (Go name → Rust):
//!
//! - `Node` → [`Node`] (`namespace`, `key`, `type`, `properties`, `state`, `owner`, `links`; `Default`).
//! - `Import(ctx, g, in)` → [`import`]`(ctx, &g, Node) -> Result<goap_domain::Node>`.
//! - `Edit(ctx, g, node, props, links...)` → [`edit`]`(ctx, &g, &NodeID, Map, &[LinkWrite]) -> Result<Node>`.
//! - `Link(ctx, g, typ, from, to)` → [`link`]`(ctx, &g, typ, &NodeRef, &NodeRef) -> Result<Node>`.
//! - `Accept(ctx, g, change, impact)` → [`accept`]`(ctx, &g, &ChangeID, &ChangeImpactID) -> Result<()>`.
//! - `Project(ctx, g, key, name)` → [`project`]; `Unit(ctx, g, key, name)` → [`unit`].
//!
//! Errors are `goap_graph::Error` (`goap_graph::Result`). The commits are boxed (`Box::pin`) so that a test driving
//! several fixtures in one future keeps a small stack.

use goap_ctx::Ctx;
use goap_domain::{
    self as domain, BaselineID, Change, ChangeID, ChangeImpactID, NodeID, NodeRef,
    STRUCTURE_ORGANISATION, STRUCTURE_PROJECT,
};
use goap_graph::{
    Graph, LinkWrite, NewChange, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, Result,
};
use serde_json::{Map, Value};

/// Node is a node a fixture lands: namespace (default `domain::DEFAULT_NAMESPACE`), key (required), type, the
/// properties of its first version, its outgoing links, the lifecycle state it is moved to and the unit owning it
/// (its key).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Node {
    pub namespace: String,
    pub key: String,
    pub r#type: String,
    pub properties: Map<String, Value>,
    pub state: String,
    pub owner: String,
    pub links: Vec<LinkWrite>,
}

/// Opens a change of its own on the head of main of a namespace.
async fn open(ctx: &Ctx, g: &Graph, ns: &str, title: &str) -> Result<Change> {
    let head = g.branch_head(ctx, ns, domain::MAIN_BRANCH).await?;
    let baseline_id: BaselineID = head.id;
    g.create_change(
        ctx,
        NewChange {
            namespace: ns.to_string(),
            title: title.to_string(),
            intent: title.to_string(),
            baseline_id,
            own_branch: true,
            project_id: g.structure(STRUCTURE_PROJECT).root,
            ..Default::default()
        },
    )
    .await
}

/// Import lands a node on main through a change of its own and returns its latest version. It names no required
/// parent: a fixture adds structure links with `links`, or afterwards ([`link`]).
pub async fn import(ctx: &Ctx, g: &Graph, input: Node) -> Result<domain::Node> {
    let ns = domain::namespace_of(&input.namespace).to_string();
    let c = open(ctx, g, &ns, &format!("Import {}", input.key)).await?;
    let cn = g
        .impact_node_create(
            ctx,
            &c.id,
            NodeCreate {
                key: input.key.clone(),
                r#type: input.r#type,
                properties: input.properties,
                owner: input.owner,
                rationale: format!("Import {}", input.key),
                links: input.links,
                ..Default::default()
            },
        )
        .await?;
    if !input.state.is_empty() {
        let post = cn.post.clone().unwrap_or_default();
        let n = g.change_node(ctx, &c.id, "", &post).await?;
        if n.state != input.state {
            g.impact_node_transition(
                ctx,
                &c.id,
                NodeTransition {
                    checkout: NodeCheckout {
                        impact: cn.id.clone(),
                        ..Default::default()
                    },
                    to: input.state,
                },
            )
            .await?;
        }
    }
    accept(ctx, g, &c.id, &cn.id).await?;
    Box::pin(g.apply(ctx, &c.id, "")).await?;
    g.node_by_key(ctx, &ns, &input.key).await
}

/// Edit lands an edit of the latest version of a node on main through a change of its own: checked out, its
/// properties merged, the links added, accepted, applied. It returns the new version (on an error after the node was
/// read, Go returned that version with it; here the error alone).
pub async fn edit(
    ctx: &Ctx,
    g: &Graph,
    node: &NodeID,
    props: Map<String, Value>,
    links: &[LinkWrite],
) -> Result<domain::Node> {
    let latest = NodeRef {
        id: node.clone(),
        ..Default::default()
    };
    let cur = g.node(ctx, &latest).await?;
    let c = open(
        ctx,
        g,
        domain::namespace_of(&cur.namespace),
        &format!("Edit {}", cur.key),
    )
    .await?;
    let cn = g
        .impact_node_checkout(
            ctx,
            &c.id,
            NodeCheckout {
                node: node.clone(),
                rationale: format!("Edit {}", cur.key),
                ..Default::default()
            },
        )
        .await?;
    if !props.is_empty() {
        g.impact_node_update(
            ctx,
            &c.id,
            &cn.id,
            NodeUpdate {
                properties: props,
                ..Default::default()
            },
        )
        .await?;
    }
    for l in links {
        g.impact_link_create(ctx, &c.id, &cn.id, l.clone(), "", "")
            .await?;
    }
    accept(ctx, g, &c.id, &cn.id).await?;
    Box::pin(g.apply(ctx, &c.id, "")).await?;
    g.node(ctx, &latest).await
}

/// Link lands an outgoing link of the latest version of a node on main ([`edit`]): the source gets a new version.
pub async fn link(
    ctx: &Ctx,
    g: &Graph,
    typ: &str,
    from: &NodeRef,
    to: &NodeRef,
) -> Result<domain::Node> {
    edit(
        ctx,
        g,
        &from.id,
        Map::new(),
        &[LinkWrite {
            r#type: typ.to_string(),
            to: to.clone(),
            ..Default::default()
        }],
    )
    .await
}

/// Accept accepts a change impact of the main flow when it is proposed: a fixture accepts its own drafts.
pub async fn accept(
    ctx: &Ctx,
    g: &Graph,
    change: &ChangeID,
    impact: &ChangeImpactID,
) -> Result<()> {
    let impacts = g.list_change_impacts(ctx, change).await?;
    if impacts
        .iter()
        .any(|cn| cn.id == *impact && cn.review == domain::REVIEW_PROPOSED)
    {
        g.impact_node_review_on(
            ctx,
            change,
            domain::MAIN_FLOW,
            "",
            impact,
            domain::REVIEW_ACCEPTED,
            "tester",
            "ok",
        )
        .await?;
    }
    Ok(())
}

/// Project lands a project under the root project of the graph (ADR 0039: a project is created with its parent).
pub async fn project(ctx: &Ctx, g: &Graph, key: &str, name: &str) -> Result<domain::Node> {
    under(ctx, g, STRUCTURE_PROJECT, key, name).await
}

/// Unit lands a unit of the organisation under its root unit (ADR 0054: a unit is created with its parent).
pub async fn unit(ctx: &Ctx, g: &Graph, key: &str, name: &str) -> Result<domain::Node> {
    under(ctx, g, STRUCTURE_ORGANISATION, key, name).await
}

async fn under(ctx: &Ctx, g: &Graph, kind: &str, key: &str, name: &str) -> Result<domain::Node> {
    g.bootstrap(ctx).await?;
    let st = g.structure(kind);
    let root = g.node_by_key(ctx, &st.namespace, &st.root).await?;
    let mut properties = Map::new();
    properties.insert("name".into(), Value::String(name.to_string()));
    Box::pin(import(
        ctx,
        g,
        Node {
            namespace: st.namespace.clone(),
            key: key.to_string(),
            r#type: st.r#type.clone(),
            properties,
            links: vec![LinkWrite {
                r#type: st.parent.clone(),
                to: root.r#ref(),
                ..Default::default()
            }],
            ..Default::default()
        },
    ))
    .await
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    fn graph() -> Graph {
        Graph::new(Arc::new(goap_graph::Memory::new()))
    }

    #[tokio::test]
    async fn import_edit_link_and_structures() {
        let ctx = Ctx::background();
        let g = graph();
        let u = unit(&ctx, &g, "ORG-A", "A").await.unwrap();
        assert_eq!(u.key, "ORG-A");
        assert_eq!(u.version, 1);
        let p = project(&ctx, &g, "PROJ-A", "Project A").await.unwrap();
        assert_eq!(p.properties.get("name"), Some(&Value::from("Project A")));

        let mut props = Map::new();
        props.insert("title".into(), Value::from("one"));
        let a = import(
            &ctx,
            &g,
            Node {
                key: "N-1".into(),
                r#type: "Thing".into(),
                properties: props,
                ..Default::default()
            },
        )
        .await
        .unwrap();
        let b = import(
            &ctx,
            &g,
            Node {
                key: "N-2".into(),
                r#type: "Thing".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
        let mut more = Map::new();
        more.insert("title".into(), Value::from("two"));
        let a2 = edit(&ctx, &g, &a.id, more, &[]).await.unwrap();
        assert_eq!(a2.version, 2);
        assert_eq!(a2.properties.get("title"), Some(&Value::from("two")));
        let a3 = link(&ctx, &g, "relates", &a2.r#ref(), &b.r#ref())
            .await
            .unwrap();
        assert_eq!(a3.version, 3);
    }
}
