//! A change works on its own branch and merges into the one it was forked from
//! (ADR 0056, 0101, 0102).

use goap_domain::*;
use goap_graphapi::ChangePatch;
use serde_json::{Map, Value, json};

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{Graph, NewChange};

/// setProp opens a change on its own branch from base that modifies REQ-1 with props, accepted.
pub(crate) async fn set_prop(
    g: &Graph,
    f: &Fixture,
    base: &BaselineID,
    title: &str,
    p: Map<String, Value>,
) -> Change {
    let ctx = ctx();
    let c = must(
        g.create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: title.into(),
                baseline_id: base.clone(),
                own_branch: true,
                ..Default::default()
            },
        )
        .await,
    );
    let ns = must(
        g.propose_impact(
            &ctx,
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(f.req.r#ref()),
                rationale: title.into(),
                ..Default::default()
            }],
        )
        .await,
    );
    must(
        edit(
            g,
            &c.id,
            &ns[0].id,
            Edit {
                properties: p,
                ..Default::default()
            },
        )
        .await,
    );
    must(accept(g, &c.id, &ns[0].id, "u", "ok").await);
    c
}

/// settle keeps the values of a change in its conflicts (ImpactNodeResolve) and accepts what is proposed again.
pub(crate) async fn settle(g: &Graph, id: &ChangeID) {
    let ctx = ctx();
    let st = must(g.rebase_state(&ctx, id).await);
    for c in &st.conflicts {
        must(g.impact_node_resolve(&ctx, id, &c.impact, "").await);
    }
    must(accept_all(g, id).await);
}

/// The latest version of a node on main.
pub(crate) async fn head_node(g: &Graph, id: &NodeID) -> Node {
    must(
        g.node(
            &ctx(),
            &NodeRef {
                id: id.clone(),
                version: 0,
            },
        )
        .await,
    )
}

#[tokio::test]
async fn change_branch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let a = set_prop(
            g,
            &f,
            &f.base.id,
            "A",
            props(&[("title", json!("A title")), ("owner", json!("x"))]),
        )
        .await;
        let b = set_prop(
            g,
            &f,
            &f.base.id,
            "B",
            props(&[("title", json!("B title")), ("owner", json!("x"))]),
        )
        .await;
        assert!(
            a.branch != "main" && a.branch != b.branch,
            "each change needs its own branch: {:?} {:?}",
            a.branch,
            b.branch
        );
        // versions live on the change branch until merged: main still sees v1
        let shared = must(g.shared_nodes(&ctx, &a.id).await);
        assert!(
            shared.len() == 1 && shared[0].changes[0] == b.id,
            "shared nodes = {shared:?}"
        );

        // A applies: merged into main (no conflict)
        let ba = must(apply(g, &a.id, "").await);
        assert_eq!(ba.branch, "main", "result baseline branch");
        let ac = must(g.change(&ctx, &a.id).await);
        assert!(
            ac.status == CHANGE_APPLIED && ac.result_baseline_id == ba.id,
            "A = {} {}",
            ac.status,
            ac.result_baseline_id
        );
        let br = must(g.branch(&ctx, "", &a.branch).await);
        assert_eq!(br.status, BRANCH_MERGED, "A branch");
        let n = head_node(g, &f.req.id).await;
        assert_eq!(n.properties["title"], "A title", "main not updated");

        // B changes the same property from the same base: the apply commits it, rebases its branch onto A and stops
        // on the conflict (ADR 0102)
        let err = apply(g, &b.id, "").await.unwrap_err();
        assert!(err.is_conflict(), "apply with a conflict: {err}");
        let bc = must(g.change(&ctx, &b.id).await);
        assert!(
            bc.status == CHANGE_ACTIVE && bc.baseline_id == ba.id,
            "B = {} on {}, want active, rebased onto {}",
            bc.status,
            bc.baseline_id,
            ba.id
        );
        let n = head_node(g, &f.req.id).await;
        assert_eq!(
            n.properties["title"], "A title",
            "pending change leaked on main"
        );
        // the author settles it (B keeps its values) and applies: its merge version (B's commit and A's version as
        // parents) is committed, then a fast-forward on top of A
        settle(g, &b.id).await;
        let bb = must(apply(g, &b.id, "").await);
        assert!(
            bb.kind == BASELINE_FAST_FORWARD && bb.parent_id == ba.id,
            "B's baseline = {} on {}, want a fast-forward of {}",
            bb.kind,
            bb.parent_id,
            ba.id
        );
        let n = head_node(g, &f.req.id).await;
        assert!(
            n.properties["title"] == "B title" && n.parents.len() == 2,
            "B not landed on A: {:?} {:?}",
            n.properties,
            n.parents
        );
    })
    .await;
}

#[tokio::test]
async fn change_branch_disjoint_props() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let a = set_prop(
            g,
            &f,
            &f.base.id,
            "A",
            props(&[("title", json!("Use PSP v1")), ("a", json!(1))]),
        )
        .await;
        let b = set_prop(
            g,
            &f,
            &f.base.id,
            "B",
            props(&[("title", json!("Use PSP v1")), ("b", json!(2))]),
        )
        .await;
        for c in [&a, &b] {
            must(apply(g, &c.id, "").await);
            let got = must(g.change(&ctx, &c.id).await);
            assert_eq!(got.status, CHANGE_APPLIED, "{}", c.title);
        }
        let n = head_node(g, &f.req.id).await;
        assert!(
            n.properties.get("a").and_then(Value::as_f64) == Some(1.0)
                && n.properties.get("b").and_then(Value::as_f64) == Some(2.0),
            "both changes must survive the 3-way merge: {:?}",
            n.properties
        );
    })
    .await;
}

#[tokio::test]
async fn abandon_change_branch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let c = set_prop(&f.g, &f, &f.base.id, "A", props(&[("x", json!(1))])).await;
        must(
            f.g.update_change(
                &ctx,
                &c.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        let b = must(f.g.branch(&ctx, "", &c.branch).await);
        assert_eq!(b.status, BRANCH_ABANDONED, "branch");
    })
    .await;
}
