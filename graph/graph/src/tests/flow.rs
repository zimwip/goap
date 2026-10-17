//! The flows of the items of a change (ADR 0025): a relaunch makes the items it replaces
//! stale, its candidates are seen on the flow only, and the flow is adopted, discarded or loses to a competing one.

use std::collections::HashSet;

use goap_domain::*;
use goap_graphapi::OpenFlowRequest;
use serde_json::{Map, Value, json};

use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

/// upd is a fact of the blackboard (an artifact) that stands for what a step produced.
pub(crate) fn upd(id: &str, title: &str, derived: &[&str]) -> ChangeItem {
    let mut data = Map::new();
    data.insert("title".into(), Value::from(title));
    ChangeItem {
        id: ItemID::from(id),
        r#type: TYPE_ARTIFACT,
        target: "result".into(),
        derived_from: derived.iter().map(|d| ItemID::from(*d)).collect(),
        data,
        ..Default::default()
    }
}

async fn status_of(g: &Graph, c: &ChangeID, item: &str) -> ItemStatus {
    must(g.change(&ctx(), c).await).effective_status(&ItemID::from(item))
}

async fn new_change(g: &Graph, base: &Baseline, t: &str) -> Change {
    must(
        g.create_change(
            &ctx(),
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: t.into(),
                baseline_id: base.id.clone(),
                ..Default::default()
            },
        )
        .await,
    )
}

fn seeds(ids: &[&str]) -> Vec<ItemID> {
    ids.iter().map(|s| ItemID::from(*s)).collect()
}

async fn seen(g: &Graph, c: &ChangeID, flow: &str) -> HashSet<ItemID> {
    let bb = must(g.blackboard_in(&ctx(), c, flow).await);
    bb.change.items.iter().map(|it| it.id.clone()).collect()
}

async fn add_flow_item(g: &Graph, c: &ChangeID, flow: &str, mut it: ChangeItem) {
    it.flow = flow.into();
    must(g.add_items(&ctx(), c, vec![it]).await);
}

#[tokio::test]
async fn flow_relaunch_adopt() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = new_change(g, &f.base, "flow").await;
        // step 1 produced p1; step 2 produced p2 from p1; p3 came from elsewhere
        must(
            g.add_items(
                &ctx,
                &c.id,
                vec![
                    upd("p1", "old", &[]),
                    upd("p2", "derived from p1", &["p1"]),
                    upd("p3", "independent", &[]),
                ],
            )
            .await,
        );
        let fl = must(
            g.open_flow(
                &ctx,
                &c.id,
                OpenFlowRequest {
                    seeds: seeds(&["p1"]),
                    origin: json!({"step": 1, "reason": "the PSP answer changed"})
                        .as_object()
                        .cloned()
                        .unwrap_or_default(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            fl.stale.len() == 2 && fl.status == FLOW_OPEN,
            "flow = {fl:?}"
        );
        // the origin is stored and returned as is (the graph never reads it)
        let full = must(g.change(&ctx, &c.id).await);
        assert_eq!(
            full.flows()[0].origin.get("reason"),
            Some(&Value::from("the PSP answer changed")),
            "origin"
        );
        for (item, want) in [
            ("p1", ITEM_STALE),
            ("p2", ITEM_STALE),
            ("p3", ITEM_PROPOSED),
        ] {
            assert_eq!(status_of(g, &c.id, item).await, want, "{item}");
        }
        // no apply while a flow is open
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "apply with an open flow",
        );

        // the replanned run appends candidates on the flow
        let mut cand = upd("c1", "new", &[]);
        cand.flow = fl.id.clone();
        must(g.add_items(&ctx, &c.id, vec![cand.clone()]).await);
        assert_eq!(status_of(g, &c.id, "c1").await, ITEM_CANDIDATE, "c1");
        let main = must(g.blackboard(&ctx, &c.id).await);
        assert!(
            !main.change.items.iter().any(|it| it.id == "c1"),
            "main blackboard must not show candidates"
        );
        let v = seen(g, &c.id, &fl.id).await;
        let has = |s: &str| v.contains(&ItemID::from(s));
        assert!(
            !has("p1") && !has("p2") && has("p3") && has("c1"),
            "flow view = {v:?}"
        );
        // a batch cannot mix flows, nor target a closed flow
        assert_kind(
            g.add_items(&ctx, &c.id, vec![cand.clone(), upd("x", "y", &[])])
                .await,
            ErrorKind::Invalid,
            "mixed batch",
        );

        must(g.adopt_flow(&ctx, &c.id, &fl.id, "alice").await);
        for (item, want) in [
            ("p1", ITEM_SUPERSEDED),
            ("p2", ITEM_SUPERSEDED),
            ("p3", ITEM_PROPOSED),
            ("c1", ITEM_PROPOSED),
        ] {
            assert_eq!(status_of(g, &c.id, item).await, want, "after adopt {item}");
        }
        // the flow is adopted: nothing holds the change any more, but its items modify no node (ADR 0102)
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::NothingToCommit,
            "a change of items only has nothing to commit",
        );
        let fs = must(g.flows(&ctx, &c.id).await);
        assert!(
            fs.len() == 1 && fs[0].status == FLOW_ADOPTED && fs[0].decided_by == "alice",
            "flows = {fs:?}"
        );
    })
    .await;
}

#[tokio::test]
async fn flow_discard() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = new_change(g, &f.base, "flow").await;
        must(g.add_items(&ctx, &c.id, vec![upd("p1", "old", &[])]).await);
        let fl = must(
            g.open_flow(
                &ctx,
                &c.id,
                OpenFlowRequest {
                    seeds: seeds(&["p1"]),
                    ..Default::default()
                },
            )
            .await,
        );
        let mut cand = upd("c1", "new", &[]);
        cand.flow = fl.id.clone();
        must(g.add_items(&ctx, &c.id, vec![cand.clone()]).await);
        must(g.discard_flow(&ctx, &c.id, &fl.id, "bob").await);
        assert_eq!(
            status_of(g, &c.id, "p1").await,
            ITEM_PROPOSED,
            "p1: the old run counts again"
        );
        assert_eq!(status_of(g, &c.id, "c1").await, ITEM_REJECTED, "c1");
        assert_kind(
            g.add_items(&ctx, &c.id, vec![cand]).await,
            ErrorKind::Conflict,
            "adding to a discarded flow",
        );
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::NothingToCommit,
            "the flow is decided, the items modify no node",
        );
        // a new relaunch is possible once the flow is decided
        let c2 = new_change(g, &f.base, "flow2").await;
        must(g.add_items(&ctx, &c2.id, vec![upd("q1", "t", &[])]).await);
        assert_kind(
            g.open_flow(
                &ctx,
                &c2.id,
                OpenFlowRequest {
                    seeds: seeds(&["nope"]),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Invalid,
            "unknown seed",
        );
    })
    .await;
}

#[tokio::test]
async fn parallel_flows_compete() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = new_change(g, &f.base, "parallel").await;
        must(
            g.add_items(
                &ctx,
                &c.id,
                vec![
                    upd("p1", "old", &[]),
                    upd("p2", "derived", &["p1"]),
                    upd("p3", "independent", &[]),
                ],
            )
            .await,
        );
        // three alternatives open at the same time: two replace p1, one replaces p3
        let open = async |s: &str| {
            must(
                g.open_flow(
                    &ctx,
                    &c.id,
                    OpenFlowRequest {
                        seeds: seeds(&[s]),
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let f1 = open("p1").await;
        let f2 = open("p1").await;
        let f3 = open("p3").await;
        add_flow_item(g, &c.id, &f1.id, upd("c1", "one", &[])).await;
        add_flow_item(g, &c.id, &f2.id, upd("c2", "two", &[])).await;
        add_flow_item(g, &c.id, &f3.id, upd("c3", "three", &[])).await;

        // each flow reads its own view: the other alternatives are invisible to it
        let v = seen(g, &c.id, &f1.id).await;
        let has = |v: &HashSet<ItemID>, s: &str| v.contains(&ItemID::from(s));
        assert!(
            has(&v, "c1") && !has(&v, "c2") && !has(&v, "p1") && has(&v, "p3"),
            "view of flow 1 = {v:?}"
        );
        let v = seen(g, &c.id, &f2.id).await;
        assert!(!has(&v, "c1") && has(&v, "c2"), "view of flow 2 = {v:?}");

        must(g.adopt_flow(&ctx, &c.id, &f1.id, "alice").await);
        let fs = must(g.flows(&ctx, &c.id).await);
        let by_id = |id: &str| fs.iter().find(|fl| fl.id == id).cloned().expect("flow");
        assert_eq!(
            by_id(&f2.id).competes_with,
            vec![f1.id.clone()],
            "flow 2 must compete with flow 1"
        );
        assert!(
            by_id(&f3.id).competes_with.is_empty(),
            "flow 3 replaces other items"
        );
        assert_kind(
            g.adopt_flow(&ctx, &c.id, &f2.id, "alice").await,
            ErrorKind::Conflict,
            "adopting a competing flow",
        );
        must(g.discard_flow(&ctx, &c.id, &f2.id, "alice").await);
        must(g.adopt_flow(&ctx, &c.id, &f3.id, "alice").await);
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::NothingToCommit,
            "every flow is decided, the items modify no node",
        );
    })
    .await;
}

#[tokio::test]
async fn flow_guidance_is_private_to_the_branch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = new_change(g, &f.base, "guided").await;
        must(g.add_items(&ctx, &c.id, vec![upd("p1", "old", &[])]).await);
        let mut data = Map::new();
        data.insert("text".into(), Value::from("keep PSP v1 compatibility"));
        let fl = must(
            g.open_flow(
                &ctx,
                &c.id,
                OpenFlowRequest {
                    seeds: seeds(&["p1"]),
                    items: vec![ChangeItem {
                        r#type: TYPE_ARTIFACT,
                        target: "guidance".into(),
                        produced_by: "alice".into(),
                        data,
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.open_flow(
                &ctx,
                &c.id,
                OpenFlowRequest {
                    items: vec![ChangeItem {
                        r#type: TYPE_FLOW,
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Invalid,
            "a flow item at the opening of a flow",
        );
        let has = async |flow: &str| {
            let bb = must(g.blackboard_in(&ctx, &c.id, flow).await);
            bb.change.items.iter().any(|it| {
                it.target == "guidance"
                    && it.data.get("text") == Some(&Value::from("keep PSP v1 compatibility"))
                    && it.produced_by == "alice"
            })
        };
        assert!(
            has(&fl.id).await && !has("").await,
            "guidance must be on the branch only"
        );
        // discarded: it disappears with the branch
        must(g.discard_flow(&ctx, &c.id, &fl.id, "alice").await);
        assert!(!has("").await, "guidance leaked on the main flow");
    })
    .await;
}
