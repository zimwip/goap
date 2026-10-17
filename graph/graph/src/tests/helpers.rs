//! The fixtures of the G2 tests (Go: `checkout_helpers_test.go`): they write through the operations every change
//! uses (ADR 0076), and land through the stand-ins [`apply`] / [`commit_change`] of testsupport until slice G3 ports
//! the commit and the merge.

use goap_domain::{
    ChangeID, ChangeImpact, ChangeImpactID, Draft, INTENT_CREATED, Link, LinkID, MAIN_BRANCH,
    MAIN_FLOW, Node, NodeID, NodeRef, REVIEW_ACCEPTED, REVIEW_PROPOSED, REVIEW_REJECTED,
    namespace_of,
};
use goap_graphapi::{LinkWrite, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate};
use serde_json::{Map, Value};

use crate::draft::own_draft;
use crate::flownodes::FlowNodes;
use crate::repo::Tx;
use crate::testsupport::*;
use crate::{Graph, NewChange, Result};

/// ImportNode (Go: newNode) is a node a fixture lands: namespace (default: the default namespace), key (default: an id), type, the
/// properties of its first version, the lifecycle state it is moved to and the unit owning it.
#[derive(Default, Clone)]
pub(crate) struct ImportNode {
    pub(crate) namespace: String,
    pub(crate) key: String,
    pub(crate) r#type: String,
    pub(crate) properties: Map<String, Value>,
    pub(crate) state: String,
    pub(crate) owner: String,
    /// The outgoing links of its first version.
    pub(crate) links: Vec<LinkWrite>,
}

async fn own_change(g: &Graph, ns: &str, title: String) -> Result<goap_domain::Change> {
    let head = g.branch_head(&ctx(), ns, MAIN_BRANCH).await?;
    g.create_change(
        &ctx(),
        NewChange {
            project_id: "PROJ-ROOT".into(),
            namespace: ns.into(),
            title: title.clone(),
            intent: title,
            baseline_id: head.id,
            own_branch: true,
            ..Default::default()
        },
    )
    .await
}

/// importNode lands a node on main through a change of its own: created, moved to its state, accepted, applied. It
/// names no required parent: fixtures add structure links afterwards (import_link).
pub(crate) async fn import_node(g: &Graph, mut input: ImportNode) -> Result<Node> {
    let ctx = ctx();
    let ns = namespace_of(&input.namespace).to_string();
    if input.key.is_empty() {
        input.key = g.new_id();
    }
    let c = own_change(g, &ns, format!("Import {}", input.key)).await?;
    let cn = g
        .impact_node_create(
            &ctx,
            &c.id,
            NodeCreate {
                key: input.key.clone(),
                r#type: input.r#type.clone(),
                properties: input.properties.clone(),
                owner: input.owner.clone(),
                rationale: format!("Import {}", input.key),
                links: input.links.clone(),
                ..Default::default()
            },
        )
        .await?;
    if !input.state.is_empty() {
        let post = cn.post.clone().expect("post");
        let n = g.change_node(&ctx, &c.id, "", &post).await?;
        if n.state != input.state {
            g.impact_node_transition(
                &ctx,
                &c.id,
                NodeTransition {
                    checkout: NodeCheckout {
                        impact: cn.id.clone(),
                        ..Default::default()
                    },
                    to: input.state.clone(),
                },
            )
            .await?;
        }
    }
    accept_impact(g, &c.id, &cn.id, "").await?;
    apply(g, &c.id, "").await?;
    g.node_by_key(&ctx, &ns, &input.key).await
}

/// importLink adds an outgoing link to the latest version of a node on main through a change of its own: the node is
/// checked out, linked, accepted and the change applied. The link of the new version is returned.
pub(crate) async fn import_link(
    g: &Graph,
    typ: &str,
    from: &NodeRef,
    to: &NodeRef,
    props: Map<String, Value>,
) -> Result<Link> {
    let ctx = ctx();
    let src = g
        .node(
            &ctx,
            &NodeRef {
                id: from.id.clone(),
                version: 0,
            },
        )
        .await?;
    let ns = namespace_of(&src.namespace).to_string();
    let c = own_change(g, &ns, format!("Link {}", src.key)).await?;
    let cn = g
        .impact_node_checkout(
            &ctx,
            &c.id,
            NodeCheckout {
                node: from.id.clone(),
                rationale: format!("Link {}", src.key),
                ..Default::default()
            },
        )
        .await?;
    let l = g
        .impact_link_create(
            &ctx,
            &c.id,
            &cn.id,
            LinkWrite {
                r#type: typ.into(),
                to: to.clone(),
                properties: props,
            },
            "",
            "",
        )
        .await?;
    accept_impact(g, &c.id, &cn.id, "").await?;
    apply(g, &c.id, "").await?;
    Ok(l)
}

/// importProps sets properties of the latest version of a node on main through a change of its own and returns the
/// new version.
pub(crate) async fn import_props(
    g: &Graph,
    r: &NodeRef,
    props: Map<String, Value>,
) -> Result<Node> {
    let ctx = ctx();
    let latest = NodeRef {
        id: r.id.clone(),
        version: 0,
    };
    let src = g.node(&ctx, &latest).await?;
    let ns = namespace_of(&src.namespace).to_string();
    let c = own_change(g, &ns, format!("Edit {}", src.key)).await?;
    let cn = g
        .impact_node_checkout(
            &ctx,
            &c.id,
            NodeCheckout {
                node: r.id.clone(),
                rationale: format!("Edit {}", src.key),
                ..Default::default()
            },
        )
        .await?;
    g.impact_node_update(
        &ctx,
        &c.id,
        &cn.id,
        NodeUpdate {
            properties: props,
            ..Default::default()
        },
    )
    .await?;
    accept_impact(g, &c.id, &cn.id, "").await?;
    apply(g, &c.id, "").await?;
    g.node(&ctx, &latest).await
}

/// acceptImpact accepts a change impact on a flow when it is proposed.
pub(crate) async fn accept_impact(
    g: &Graph,
    id: &ChangeID,
    impact: &ChangeImpactID,
    flow: &str,
) -> Result<()> {
    let seen = seen_impact(g, id, impact, flow).await?;
    if seen.review == REVIEW_PROPOSED {
        g.impact_node_review_on(
            &ctx(),
            id,
            flow,
            "",
            impact,
            REVIEW_ACCEPTED,
            "tester",
            "ok",
        )
        .await?;
    }
    Ok(())
}

/// checkedOut reports whether a change impact has a draft of its own on a flow.
pub(crate) async fn checked_out(
    g: &Graph,
    id: &ChangeID,
    impact: &ChangeImpactID,
    flow: &str,
) -> Result<bool> {
    let ctx = ctx();
    let mut tx = g.begin(&ctx).await?;
    let c = tx.change(&ctx, id).await?;
    let rows = g.drafts(&ctx, &mut tx, id).await?;
    Ok(own_draft(&rows, impact, &c.resolve_flow(flow)).is_some())
}

/// seenImpact is a change impact as a flow sees it.
pub(crate) async fn seen_impact(
    g: &Graph,
    id: &ChangeID,
    impact: &ChangeImpactID,
    flow: &str,
) -> Result<ChangeImpact> {
    let ctx = ctx();
    let mut tx = g.begin(&ctx).await?;
    let c = tx.change(&ctx, id).await?;
    let flow = c.resolve_flow(flow);
    FlowNodes::new(c, &flow).find(&ctx, &mut tx, impact).await
}

/// Edit is an edit of a change impact in a test: its properties, owner and links are edited on its draft (checked out
/// when it is not); a state is a transition.
#[derive(Default, Clone)]
pub(crate) struct Edit {
    pub(crate) properties: Map<String, Value>,
    pub(crate) state: String,
    pub(crate) add_links: Vec<LinkWrite>,
    pub(crate) remove_links: Vec<LinkID>,
    pub(crate) flow: String,
    pub(crate) execution: String,
    pub(crate) owner: String,
}

/// edit applies an edit to a change impact and returns it as the flow sees it.
pub(crate) async fn edit(
    g: &Graph,
    id: &ChangeID,
    impact: &ChangeImpactID,
    e: Edit,
) -> Result<ChangeImpact> {
    let ctx = ctx();
    if !e.properties.is_empty()
        || !e.owner.is_empty()
        || !e.add_links.is_empty()
        || !e.remove_links.is_empty()
    {
        if !checked_out(g, id, impact, &e.flow).await? {
            g.impact_node_checkout(
                &ctx,
                id,
                NodeCheckout {
                    impact: impact.clone(),
                    flow: e.flow.clone(),
                    execution: e.execution.clone(),
                    ..Default::default()
                },
            )
            .await?;
        }
        if !e.properties.is_empty() || !e.owner.is_empty() {
            g.impact_node_update(
                &ctx,
                id,
                impact,
                NodeUpdate {
                    properties: e.properties.clone(),
                    owner: e.owner.clone(),
                    flow: e.flow.clone(),
                    execution: e.execution.clone(),
                    ..Default::default()
                },
            )
            .await?;
        }
        for l in &e.add_links {
            g.impact_link_create(&ctx, id, impact, l.clone(), &e.flow, &e.execution)
                .await?;
        }
        for link in &e.remove_links {
            g.impact_link_delete(&ctx, id, link, &e.flow, &e.execution)
                .await?;
        }
    }
    if !e.state.is_empty() {
        g.impact_node_transition(
            &ctx,
            id,
            NodeTransition {
                checkout: NodeCheckout {
                    impact: impact.clone(),
                    flow: e.flow.clone(),
                    execution: e.execution.clone(),
                    ..Default::default()
                },
                to: e.state.clone(),
            },
        )
        .await?;
    }
    seen_impact(g, id, impact, &e.flow).await
}

/// acceptAll accepts every proposed change impact of the main flow of a change.
pub(crate) async fn accept_all(g: &Graph, id: &ChangeID) -> Result<()> {
    let c = g.change(&ctx(), id).await?;
    for cn in &c.nodes {
        if !cn.flow.is_empty() || cn.superseded || cn.review == REVIEW_REJECTED {
            continue;
        }
        accept_impact(g, id, &cn.id, MAIN_FLOW).await?;
    }
    Ok(())
}

/// accept accepts a change impact: what a test applies next lands it.
pub(crate) async fn accept(
    g: &Graph,
    id: &ChangeID,
    impact: &ChangeImpactID,
    by: &str,
    comment: &str,
) -> Result<ChangeImpact> {
    accept_on(g, id, "", "", impact, by, comment).await
}

/// acceptOn accepts a change impact on a flow.
pub(crate) async fn accept_on(
    g: &Graph,
    id: &ChangeID,
    flow: &str,
    execution: &str,
    impact: &ChangeImpactID,
    by: &str,
    comment: &str,
) -> Result<ChangeImpact> {
    g.impact_node_review_on(
        &ctx(),
        id,
        flow,
        execution,
        impact,
        REVIEW_ACCEPTED,
        by,
        comment,
    )
    .await?;
    seen_impact(g, id, impact, flow).await
}

/// proposeOrCreate declares impacts in a test: a node of the baseline is proposed, a new node is created, in order.
pub(crate) async fn propose_or_create(
    g: &Graph,
    id: &ChangeID,
    nodes: Vec<ChangeImpact>,
) -> Result<Vec<ChangeImpact>> {
    let mut out = Vec::new();
    for cn in nodes {
        if cn.intent == INTENT_CREATED {
            out.push(
                g.impact_node_create(
                    &ctx(),
                    id,
                    NodeCreate {
                        key: cn.key.clone(),
                        r#type: cn.r#type.clone(),
                        rationale: cn.rationale.clone(),
                        flow: cn.flow.clone(),
                        execution: cn.execution.clone(),
                        produced_by: cn.produced_by.clone(),
                        derived_from: cn.derived_from.clone(),
                        ..Default::default()
                    },
                )
                .await?,
            );
            continue;
        }
        out.extend(g.propose_impact(&ctx(), id, vec![cn]).await?);
    }
    Ok(out)
}

/// draftOf is the draft a flow sees for a change impact (None: none).
pub(crate) async fn draft_of(
    g: &Graph,
    id: &ChangeID,
    flow: &str,
    impact: &ChangeImpactID,
) -> Result<Option<Draft>> {
    let ctx = ctx();
    let mut tx = g.begin(&ctx).await?;
    let c = tx.change(&ctx, id).await?;
    let flow = c.resolve_flow(flow);
    g.seen_draft(&ctx, &mut tx, &c, &flow, impact).await
}

/// versionCount is the number of stored versions of a node (0: none).
pub(crate) async fn version_count(g: &Graph, node: &NodeID) -> usize {
    g.versions(&ctx(), node).await.map(|v| v.len()).unwrap_or(0)
}

/// touch modifies the node of a change impact (its title), so that it is a modification to review (ADR 0102).
pub(crate) async fn touch(g: &Graph, id: &ChangeID, impact: &ChangeImpactID) {
    must(
        edit(
            g,
            id,
            impact,
            Edit {
                properties: props(&[("title", Value::from("touched"))]),
                ..Default::default()
            },
        )
        .await,
    );
}

/// assertErr checks that r failed with an error of the kind whose text holds every part.
pub(crate) fn assert_err<T: std::fmt::Debug>(
    r: Result<T>,
    kind: crate::ErrorKind,
    parts: &[&str],
    what: &str,
) {
    match r {
        Ok(v) => panic!("{what}: want {kind:?} error, got {v:?}"),
        Err(e) => {
            let text = e.to_string();
            assert!(
                e.kind() == kind && parts.iter().all(|p| text.contains(p)),
                "{what}: want {kind:?} holding {parts:?}, got {:?}: {text}",
                e.kind()
            );
        }
    }
}

/// The latest version of a node on main.
pub(crate) async fn state_of(g: &Graph, id: &NodeID) -> Result<Node> {
    g.node(
        &ctx(),
        &NodeRef {
            id: id.clone(),
            version: 0,
        },
    )
    .await
}
