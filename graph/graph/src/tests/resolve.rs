//! Every operation on a node of a change resolves it the same way (ADR 0077,
//! "Resolution").

use goap_domain::*;
use goap_graphapi::{NodeCheckout, NodeCreate, NodeTransition, NodeUpdate};
use serde_json::Value;

use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::tests::lifecycle::new_lifecycle_world_g;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

pub(crate) async fn impacts_on(g: &Graph, id: &ChangeID, key: &str) -> Vec<ChangeImpact> {
    must(g.list_change_impacts(&ctx(), id).await)
        .into_iter()
        .filter(|cn| cn.key == key)
        .collect()
}

pub(crate) async fn plain(f: &Fixture, t: &str) -> Change {
    must(
        f.g.create_change(
            &ctx(),
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: t.into(),
                baseline_id: f.base.id.clone(),
                ..Default::default()
            },
        )
        .await,
    )
}

fn create(key: &str, typ: &str, rationale: &str) -> NodeCreate {
    NodeCreate {
        key: key.into(),
        r#type: typ.into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

async fn ops_of(g: &Graph, c: &ChangeID, impact: &ChangeImpactID) -> Vec<ImpactOp> {
    must(g.change_events(&ctx(), c).await)
        .into_iter()
        .filter(|e| &e.impact == impact)
        .map(|e| e.op)
        .collect()
}

#[tokio::test]
async fn resolve_create() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = plain(&f, "create").await;

        // a key the reference baseline holds
        assert_err(
            g.impact_node_create(&ctx, &c.id, create("REQ-1", "Requirement", ""))
                .await,
            ErrorKind::Conflict,
            &["baseline"],
            "create on a baseline key",
        );
        // twice
        let cn = must(
            g.impact_node_create(&ctx, &c.id, create("REQ-9", "Requirement", "new"))
                .await,
        );
        assert!(
            cn.intent == INTENT_CREATED && cn.post.is_some(),
            "created: {cn:?}"
        );
        assert_err(
            g.impact_node_create(&ctx, &c.id, create("REQ-9", "Requirement", ""))
                .await,
            ErrorKind::Conflict,
            &["already holds"],
            "create twice",
        );
        assert_eq!(impacts_on(g, &c.id, "REQ-9").await.len(), 1, "one impact");

        // a creation is planned with a key and a type (planned.rs): one without a type is refused, and nothing is
        // declared
        assert_err(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_CREATED,
                    key: "TST-9".into(),
                    rationale: "plan".into(),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Invalid,
            &["planned creation"],
            "a planned creation with no type",
        );
        assert!(
            impacts_on(g, &c.id, "TST-9").await.is_empty(),
            "a refused proposal declares nothing"
        );
        // so a node that does not exist is not found by the other operations
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "TST-9".into(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::NotFound,
            "checkout of a node that does not exist",
        );
        // the creation logs one event: created, with its impact and its first version, and no proposal
        let events = must(g.change_events(&ctx, &c.id).await);
        let ops = ops_of(g, &c.id, &cn.id).await;
        let last = events.last().expect("events");
        assert!(
            ops == vec![IMPACT_CREATED] && last.state.is_some() && last.post.is_some(),
            "a creation is one created event: {ops:?}"
        );
        // edits follow: created, updated, ...
        must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: title("x"),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            ops_of(g, &c.id, &cn.id).await,
            vec![IMPACT_CREATED, IMPACT_UPDATED],
            "created then updated"
        );
    })
    .await;
}

#[tokio::test]
async fn resolve_existing_node() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = plain(&f, "modify").await;

        // unknown nodes are in neither the baseline nor the change
        assert_err(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: NodeID::from("nope"),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::NotFound,
            &["neither"],
            "checkout of an unknown node",
        );
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "NOPE-1".into(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::NotFound,
            "checkout of an unknown key",
        );
        assert_kind(
            g.impact_node_update(
                &ctx,
                &c.id,
                &ChangeImpactID::default(),
                NodeUpdate {
                    key: "NOPE-1".into(),
                    properties: props(&[("x", Value::from(1))]),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::NotFound,
            "update of an unknown node",
        );
        assert_kind(
            g.impact_node_transition(
                &ctx,
                &c.id,
                NodeTransition {
                    checkout: NodeCheckout {
                        node: NodeID::from("nope"),
                        ..Default::default()
                    },
                    to: "x".into(),
                },
            )
            .await,
            ErrorKind::NotFound,
            "transition of an unknown node",
        );
        assert!(
            must(g.list_change_impacts(&ctx, &c.id).await).is_empty(),
            "a refused operation declares nothing"
        );

        // checkout by key declares the impact from the baseline
        let cn = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "REQ-1".into(),
                    rationale: "why".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            cn.intent == INTENT_MODIFIED
                && cn.pre.as_ref() == Some(&f.req.r#ref())
                && cn.rationale == "why",
            "auto-proposed from the baseline: {cn:?}"
        );
        // an update by node reuses it
        let up = must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &ChangeImpactID::default(),
                NodeUpdate {
                    node: f.req.id.clone(),
                    properties: title("T"),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(up.id, cn.id, "update reuses the impact");
        // create on the same key is a conflict, a second checkout too
        assert_kind(
            g.impact_node_create(&ctx, &c.id, create("REQ-1", "Requirement", ""))
                .await,
            ErrorKind::Conflict,
            "create on a node of the change",
        );
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "checkout twice",
        );
        assert_eq!(
            impacts_on(g, &c.id, "REQ-1").await.len(),
            1,
            "a node appears once"
        );
    })
    .await;
}

#[tokio::test]
async fn resolve_transition_from_baseline() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world_g(r, "").await;
        let c = w.change("move").await;
        let mv = |to: &str, rationale: &str| NodeTransition {
            checkout: NodeCheckout {
                node: w.req1.id.clone(),
                rationale: rationale.into(),
                ..Default::default()
            },
            to: to.into(),
        };
        let cn = must(
            w.g.impact_node_transition(&ctx, &c.id, mv("draft", "start"))
                .await,
        );
        assert!(
            cn.intent == INTENT_MODIFIED
                && cn.pre.as_ref() == Some(&w.req1.r#ref())
                && cn.post.is_some(),
            "auto-proposed from the baseline: {cn:?}"
        );
        // the impact is reused by the next transition
        if let Ok(cn2) =
            w.g.impact_node_transition(&ctx, &c.id, mv("approved", ""))
                .await
        {
            assert_eq!(cn2.id, cn.id, "reuse");
        }
        assert_eq!(
            impacts_on(&w.g, &c.id, &w.req1.key).await.len(),
            1,
            "a node appears once"
        );
    })
    .await;
}
