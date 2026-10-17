//! Port of `draft_test.go`: a node has no version during a change; landing writes one version per draft; a child flow
//! forks the parent's draft (ADR 0079).

use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCheckout, NodeCreate, NodeUpdate, OpenOptionRequest};
use serde_json::Value;

use crate::repo::Tx;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

pub(super) fn own_change(base: &Baseline, title: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        baseline_id: base.id.clone(),
        own_branch: true,
        ..Default::default()
    }
}

pub(super) fn title(t: &str) -> serde_json::Map<String, Value> {
    props(&[("title", Value::from(t))])
}

// A node has no version during a change (ADR 0079): the repository holds none for a creation and no new one for an
// edit, whatever the number of operations; the draft is the fold of the log, which tells every edit.
#[tokio::test]
async fn no_version_exists_during_a_change() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "drafts")).await);
        let made = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "TST-9".into(),
                    r#type: "TestCase".into(),
                    rationale: "new".into(),
                    links: vec![LinkWrite {
                        r#type: "verifies".into(),
                        to: f.req.r#ref(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        let mod_ = must(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(f.req.r#ref()),
                    rationale: "edit".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        let mut edited = ChangeImpact::default();
        for i in 0..3u8 {
            let t = format!("t{}", (b'a' + i) as char);
            edited = must(
                edit(
                    g,
                    &c.id,
                    &mod_[0].id,
                    Edit {
                        properties: title(&t),
                        ..Default::default()
                    },
                )
                .await,
            );
        }
        must(
            edit(
                g,
                &c.id,
                &made.id,
                Edit {
                    properties: title("nine"),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &c.id, &made.id, "bob", "ok").await);
        must(accept(g, &c.id, &mod_[0].id, "bob", "ok").await);

        let mut tx = must(g.begin(&ctx).await);
        let post = made.post.clone().unwrap();
        assert_kind(
            tx.versions(&ctx, &post.id).await,
            ErrorKind::NotFound,
            "a created node has no node_version row",
        );
        assert_kind(
            tx.node_id_by_key(&ctx, DEFAULT_NAMESPACE, "TST-9").await,
            ErrorKind::NotFound,
            "nor a node row, its key is free",
        );
        let vs = must(tx.versions(&ctx, &f.req.id).await);
        assert_eq!(
            vs.len() as Version,
            f.req.version,
            "an edited node gets no version"
        );
        let drafts = must(g.drafts(&ctx, &mut tx, &c.id).await);
        assert_eq!(drafts.len(), 2, "two drafts in the fold: {drafts:?}");
        drop(tx);
        // only the change reads the drafts: main still has the last landed version
        let n = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert_eq!(
            n.properties["title"], "Use PSP v1",
            "main reads the landed version"
        );
        let v = must(
            g.change_node_view(&ctx, &c.id, "", edited.post.as_ref().unwrap())
                .await,
        );
        assert!(
            v.node.properties["title"] == "tc" && v.node.is_draft(),
            "the change reads its draft: {:?}",
            v.node
        );
        let updates = must(g.change_events(&ctx, &c.id).await)
            .iter()
            .filter(|e| e.op == IMPACT_UPDATED && e.impact == mod_[0].id)
            .count();
        assert_eq!(updates, 3, "every edit is an updated event");
    })
    .await;
}

// Landing writes one version per node from its draft (ADR 0079): the next number of the node, the version it was
// checked out from as its parent, the owner and the origins of the draft, the links of the draft with their targets
// resolved to the versions this landing writes for the nodes of the change.
#[tokio::test]
async fn landing_writes_one_version_per_draft() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "land")).await);
        let pre = f.req.r#ref();
        let mod_ = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "edit".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &mod_.id,
                NodeUpdate {
                    properties: title("Use PSP v2"),
                    ..Default::default()
                },
            )
            .await,
        );
        let create = async |key: &str, to: NodeRef| {
            must(
                g.impact_node_create(
                    &ctx,
                    &c.id,
                    NodeCreate {
                        key: key.into(),
                        r#type: "TestCase".into(),
                        rationale: key.to_lowercase(),
                        links: vec![LinkWrite {
                            r#type: "verifies".into(),
                            to,
                            ..Default::default()
                        }],
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        // two new nodes link REQ-1: one by its draft, one by the version it was checked out from
        let a = create("TST-A", mod_.post.clone().unwrap()).await;
        let b = create("TST-B", pre.clone()).await;
        // a link from a created node to another one: a link among drafts
        must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &a.id,
                LinkWrite {
                    r#type: "refines".into(),
                    to: b.post.clone().unwrap(),
                    ..Default::default()
                },
                "",
                "",
            )
            .await,
        );
        let view = must(
            g.change_node_view(&ctx, &c.id, "", b.post.as_ref().unwrap())
                .await,
        );
        assert!(
            view.r#in.len() == 1 && Some(&view.r#in[0].from) == a.post.as_ref(),
            "a link between drafts is read from both ends: {:?}",
            view.r#in
        );
        for cn in [&mod_, &a, &b] {
            must(accept(g, &c.id, &cn.id, "bob", "ok").await);
        }
        let res = must(apply(g, &c.id, "").await);

        let req2 = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    version: res.nodes[&f.req.id],
                },
            )
            .await,
        );
        assert!(
            req2.version == pre.version + 1
                && req2.parents == vec![pre.version]
                && req2.owner == f.req.owner
                && req2.change_id == c.id
                && req2.change_impact == mod_.id,
            "REQ-1 v2: {req2:?}"
        );
        for (key, cn) in [("TST-A", &a), ("TST-B", &b)] {
            let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, key).await);
            assert!(
                n.version == 1
                    && n.parents.is_empty()
                    && n.reason == REASON_CREATE
                    && n.id == cn.post.as_ref().unwrap().id
                    && res.nodes.get(&n.id) == Some(&1)
                    && !n.owner.is_empty()
                    && !n.project.is_empty(),
                "{key}: {n:?}"
            );
            assert_eq!(
                version_count(g, &n.id).await,
                1,
                "exactly one version of {key}"
            );
            let verifies: Vec<_> = must(g.out_links_of(&ctx, &n.r#ref()).await)
                .into_iter()
                .filter(|l| l.r#type == "verifies")
                .collect();
            assert_eq!(verifies.len(), 1, "{key}: verifies links");
            assert_eq!(
                verifies[0].to,
                req2.r#ref(),
                "{key} verifies the version this landing wrote"
            );
        }
        let tst_a = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "TST-A").await);
        let tst_b = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "TST-B").await);
        let refines: Vec<_> = must(g.out_links_of(&ctx, &tst_a.r#ref()).await)
            .into_iter()
            .filter(|l| l.r#type == "refines")
            .map(|l| l.to)
            .collect();
        assert_eq!(
            refines,
            vec![tst_b.r#ref()],
            "a link between drafts targets the version written for the node"
        );
        // the links REQ-1 carried follow it
        let out = must(g.out_links_of(&ctx, &req2.r#ref()).await);
        assert!(
            out.len() == 1 && out[0].r#type == "satisfies",
            "carried links: {out:?}"
        );
        // every landed event of the change names the version written
        for cn in must(g.list_change_impacts(&ctx, &c.id).await) {
            assert!(
                cn.landed.is_some()
                    && cn.post.is_some()
                    && cn.post == cn.landed
                    && !cn.post.as_ref().unwrap().is_draft(),
                "post is rewritten to the version written: {cn:?}"
            );
        }
    })
    .await;
}

// A flow checks a node out once: its draft is a copy of what the flow sees, the draft of its parent flow when that
// one holds one, and the parent's later edits do not reach it (ADR 0079).
#[tokio::test]
async fn child_flow_forks_the_parent_draft() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, own_change(&f.base, "flows")).await);
        let cn = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "edit".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let update = async |t: &str, flow: &str| {
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: title(t),
                    flow: flow.into(),
                    ..Default::default()
                },
            )
            .await
        };
        must(update("main one", "").await);
        let child = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "child".into(),
                    hypothesis: "h".into(),
                    by: "u".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        // the child flow sees the main draft, but it is the main flow's to edit
        assert_kind(
            update("child", &child.id).await,
            ErrorKind::Conflict,
            "a draft of the parent flow is not edited from the child",
        );
        let checkout = async || {
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: cn.id.clone(),
                    flow: child.id.clone(),
                    ..Default::default()
                },
            )
            .await
        };
        let co = must(checkout().await);
        let d = must(draft_of(g, &c.id, &child.id, &cn.id).await);
        assert!(
            d.as_ref().is_some_and(|d| d.flow == child.id
                && d.properties["title"] == "main one"
                && d.base.as_ref() == Some(&f.req.r#ref()))
                && co.post.as_ref().unwrap().is_draft(),
            "the child's draft is a copy of the parent's: {d:?}"
        );
        // each edits its own
        must(update("main two", "").await);
        must(update("child two", &child.id).await);
        let post = co.post.clone().unwrap();
        let got = must(g.change_node(&ctx, &c.id, &child.id, &post).await);
        assert_eq!(got.properties["title"], "child two", "child");
        let got = must(g.change_node(&ctx, &c.id, "", &post).await);
        assert_eq!(got.properties["title"], "main two", "main");
        assert_kind(
            checkout().await,
            ErrorKind::Conflict,
            "once per change and flow",
        );
        // cancelling the child's draft falls back to the parent's
        let back = must(
            g.impact_node_cancel(&ctx, &c.id, &cn.id, &child.id, "")
                .await,
        );
        assert!(
            back.post.as_ref().is_some_and(|p| p.is_draft()),
            "back to the parent's draft: {back:?}"
        );
        let got = must(g.change_node(&ctx, &c.id, &child.id, &post).await);
        assert_eq!(
            got.properties["title"], "main two",
            "the child sees the parent's draft again"
        );
    })
    .await;
}
