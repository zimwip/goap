//! A node is edited through its draft (ADR 0076, 0079).

use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCheckout, NodeCreate, NodeUpdate, OpenOptionRequest};
use serde_json::Value;

use crate::repo::Tx;
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::tests::lifecycle::{new_lifecycle_world, set_title, to_state};
use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

fn plain_change(base: &Baseline, t: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: t.into(),
        baseline_id: base.id.clone(),
        ..Default::default()
    }
}

// A node is edited through its draft (ADR 0079): ImpactNodeUpdate and the link operations change it, no version is
// written during the change; an accepted review checks it and writes nothing; landing writes the version.
#[tokio::test]
async fn checkout_edit_accept() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "PSP v2")).await);
        let update = async |impact: &ChangeImpactID, p: serde_json::Map<String, Value>| {
            g.impact_node_update(
                &ctx,
                &c.id,
                impact,
                NodeUpdate {
                    properties: p,
                    ..Default::default()
                },
            )
            .await
        };
        assert!(
            update(&ChangeImpactID::new("nope"), props(&[("x", 1.into())]))
                .await
                .is_err(),
            "an update names an impact of the change"
        );
        let cn = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "new PSP".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            cn.intent == INTENT_MODIFIED
                && cn.pre.as_ref() == Some(&f.req.r#ref())
                && cn.post.as_ref().is_some_and(|p| p.is_draft() && p.id == f.req.id),
            "the checkout declares the impact and gives the node a draft: {cn:?}"
        );
        let work = cn.post.clone().unwrap();
        // the draft carries the links of the version it follows
        let out = must(g.change_node_view(&ctx, &c.id, "", &work).await).out;
        assert!(out.len() == 1 && out[0].r#type == "satisfies", "links carried: {out:?}");
        must(update(&cn.id, title("Use PSP v2")).await);
        must(update(&cn.id, props(&[("owner", "alice".into())])).await);
        let link = must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &cn.id,
                LinkWrite {
                    r#type: "verifies".into(),
                    to: f.test.r#ref(),
                    ..Default::default()
                },
                "",
                "",
            )
            .await,
        );
        must(
            g.impact_link_update(&ctx, &c.id, &link.id, props(&[("why", "spec".into())]), "", "")
                .await,
        );
        let view = must(g.change_node_view(&ctx, &c.id, "", &work).await);
        let l = must(state_of(g, &f.req.id).await);
        assert_eq!(l.version, f.req.version, "nothing is on main before the change lands");
        assert!(
            view.node.is_draft()
                && view.node.properties["title"] == "Use PSP v2"
                && view.node.properties["owner"] == "alice",
            "edited: {:?}",
            view.node
        );
        assert_eq!(
            version_count(g, &f.req.id).await as Version,
            f.req.version,
            "no version for the checkout, whatever the edits"
        );
        assert_eq!(view.out.len(), 2, "a link added to the draft: {:?}", view.out);
        // every edit is in the log, and nothing else
        let ops: Vec<ImpactOp> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .map(|e| e.op)
            .collect();
        assert_eq!(
            ops,
            vec![
                IMPACT_PROPOSED,
                IMPACT_CHECKED_OUT,
                IMPACT_UPDATED,
                IMPACT_UPDATED,
                IMPACT_UPDATED,
                IMPACT_UPDATED
            ],
            "a user edit logs the checkout and its updates, and no review"
        );
        must(edit(g, &c.id, &cn.id, Edit::default()).await);
        must(g.impact_link_delete(&ctx, &c.id, &link.id, "", "").await);
        let out = must(g.change_node_view(&ctx, &c.id, "", &work).await).out;
        assert_eq!(out.len(), 1, "a link removed from the draft: {out:?}");
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "a change with a review pending is not applied",
        );
        must(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "bob", "fine")
                .await,
        );
        assert_eq!(
            version_count(g, &f.req.id).await as Version,
            f.req.version,
            "accepting writes no version"
        );
        // a checkout of a node with a draft is refused: the caller updates it, which sends the review back to proposed
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: cn.id.clone(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "already checked out",
        );
        let again = must(update(&cn.id, title("Use PSP v3")).await);
        assert!(
            again.post.as_ref() == Some(&work) && again.review == REVIEW_PROPOSED,
            "an edit after the accept is on the draft, and the review is back to proposed: {again:?}"
        );
        must(accept(g, &c.id, &cn.id, "bob", "fine again").await);
        assert_eq!(
            version_count(g, &f.req.id).await as Version,
            f.req.version,
            "no version for the whole edit"
        );
        let res = must(apply(g, &c.id, "").await);
        let landed = must(
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
            landed.properties["title"] == "Use PSP v3"
                && landed.version == f.req.version + 1
                && landed.comment == "fine again"
                && landed.parents == vec![f.req.version],
            "landing writes the version from the draft: {landed:?}"
        );
        let out = must(g.out_links_of(&ctx, &landed.r#ref()).await);
        assert!(out.len() == 1 && out[0].r#type == "satisfies", "with its links: {out:?}");
        let d = must(draft_of(g, &c.id, "", &cn.id).await);
        assert!(d.is_none(), "the draft goes when its version is written: {d:?}");
        let got = must(seen_impact(g, &c.id, &cn.id, "").await);
        assert!(
            got.post.as_ref() == Some(&landed.r#ref()) && got.landed.as_ref() == Some(&landed.r#ref()),
            "post is rewritten to the landed version: {got:?}"
        );
        // a version never changes once written
        let mut tx = must(g.begin(&ctx).await);
        let res = tx
            .put_link(
                &ctx,
                Link {
                    id: LinkID::new(g.new_id()),
                    r#type: "x".into(),
                    from: landed.r#ref(),
                    to: landed.r#ref(),
                    change_id: c.id.clone(),
                    ..Default::default()
                },
            )
            .await;
        let res = match res {
            Ok(()) => tx.commit(&ctx).await,
            Err(e) => Err(e),
        };
        assert_kind(res, ErrorKind::Conflict, "a link added to a written version");
    })
    .await;
}

// Cancelling a checkout drops the draft (ADR 0079): a modified node goes back to the version the change saw, a
// creation cancelled removes its impact; no node was ever written.
#[tokio::test]
async fn impact_node_cancel() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "try")).await);
        let m = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "try".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let created = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-9".into(),
                    r#type: "Requirement".into(),
                    rationale: "new".into(),
                    links: vec![LinkWrite {
                        r#type: "satisfies".into(),
                        to: f.need.r#ref(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        // a link of the created node to the draft of REQ-1
        let link = must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &created.id,
                LinkWrite {
                    r#type: "refines".into(),
                    to: m.post.clone().unwrap(),
                    ..Default::default()
                },
                "",
                "",
            )
            .await,
        );
        assert_eq!(
            Some(&link.to),
            m.post.as_ref(),
            "a link between drafts targets the node by id"
        );
        let back = must(g.impact_node_cancel(&ctx, &c.id, &m.id, "", "").await);
        assert!(
            back.post.is_none(),
            "back to the impact without a draft: {back:?}"
        );
        let d = must(draft_of(g, &c.id, "", &m.id).await);
        assert!(d.is_none(), "the draft is dropped: {d:?}");
        assert_kind(
            g.impact_node_cancel(&ctx, &c.id, &m.id, "", "").await,
            ErrorKind::Conflict,
            "nothing checked out",
        );
        must(g.impact_node_cancel(&ctx, &c.id, &created.id, "", "").await);
        assert_kind(
            g.node_by_key(&ctx, DEFAULT_NAMESPACE, "REQ-9").await,
            ErrorKind::NotFound,
            "a cancelled creation leaves no node",
        );
        let impacts = must(g.list_change_impacts(&ctx, &c.id).await);
        assert!(
            impacts.len() == 1 && impacts[0].id == m.id,
            "nor its impact: {impacts:?}"
        );
        // checked out again, modified, accepted: cancelling drops the draft, the impact is back to proposed
        must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: m.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        touch(g, &c.id, &m.id).await;
        must(accept(g, &c.id, &m.id, "bob", "ok").await);
        let back = must(g.impact_node_cancel(&ctx, &c.id, &m.id, "", "").await);
        assert!(
            back.post.is_none() && back.review == REVIEW_PROPOSED,
            "back to the impact without draft, to review again: {back:?}"
        );
    })
    .await;
}

// A flow is adopted with its drafts accepted (ADR 0076, 0079).
#[tokio::test]
async fn adopt_flow_needs_accept() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    own_branch: true,
                    ..plain_change(&f.base, "options")
                },
            )
            .await,
        );
        let opt = must(
            g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "A".into(),
                    hypothesis: "a".into(),
                    by: "u".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let cn = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    node: f.req.id.clone(),
                    rationale: "A".into(),
                    flow: opt.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: title("A"),
                    flow: opt.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.adopt_flow(&ctx, &c.id, &opt.id, "u").await,
            ErrorKind::Conflict,
            "a flow with a draft it did not accept is adopted",
        );
        must(accept_on(g, &c.id, &opt.id, "", &cn.id, "u", "ok").await);
        must(g.adopt_flow(&ctx, &c.id, &opt.id, "u").await);
    })
    .await;
}

// A link a draft of the change holds to a node of the change targets the draft: it follows the node to the version
// landing writes for it (ADR 0079).
#[tokio::test]
async fn draft_links_follow_new_versions() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let g = &w.g;
        let c = w.change("follow").await;
        let req2 = w.declare(&c, &w.req2).await;
        // a new Note links the baseline version of REQ-2; REQ-2 is then moved (its draft): the link targets it by id
        let note = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "NOTE-1".into(),
                    r#type: "Note".into(),
                    rationale: "cover".into(),
                    links: vec![LinkWrite {
                        r#type: "refines".into(),
                        to: w.req2.r#ref(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        must(edit(g, &c.id, &req2, set_title("two")).await);
        must(edit(g, &c.id, &req2, to_state("draft")).await);
        let moved = must(edit(g, &c.id, &req2, to_state("approved")).await);
        let out = must(
            g.change_node_view(&ctx, &c.id, "", note.post.as_ref().unwrap())
                .await,
        )
        .out;
        assert!(
            out.len() == 1 && Some(&out[0].to) == moved.post.as_ref() && out[0].to.is_draft(),
            "the link of the draft targets the draft of REQ-2: {out:?}"
        );
        must(accept(g, &c.id, &note.id, "u", "ok").await);
        must(accept(g, &c.id, &req2, "u", "ok").await);
        let res = must(apply(g, &c.id, "").await);
        let nid = note.post.as_ref().unwrap().id.clone();
        let landed = must(
            g.out_links_of(
                &ctx,
                &NodeRef {
                    version: res.nodes[&nid],
                    id: nid,
                },
            )
            .await,
        );
        let want = NodeRef {
            id: w.req2.id.clone(),
            version: res.nodes[&w.req2.id],
        };
        assert!(
            landed.len() == 1 && landed[0].to == want && want.version == w.req2.version + 1,
            "landing resolves the link to the version it writes for REQ-2 ({}): {landed:?}",
            want.version
        );
    })
    .await;
}

// A creation gives the node a draft (event created), no version: checking it out again is refused.
#[tokio::test]
async fn create_is_checked_out() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "New")).await);
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-NEW".into(),
                    r#type: "alm@Requirement".into(),
                    rationale: "new".into(),
                    properties: title("x"),
                    ..Default::default()
                },
            )
            .await,
        );
        let post = cn.post.clone().expect("post");
        assert!(
            post.is_draft() && must(g.change_node(&ctx, &c.id, "", &post).await).is_draft(),
            "a created node starts as a draft: {cn:?}"
        );
        assert_eq!(
            version_count(g, &post.id).await,
            0,
            "a created node has no version during the change"
        );
        assert_kind(
            g.node_by_key(&ctx, DEFAULT_NAMESPACE, "REQ-NEW").await,
            ErrorKind::NotFound,
            "outside the change the node does not exist yet",
        );
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: cn.id.clone(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "checking out a checked-out node must fail with a conflict",
        );
        for e in must(g.change_events(&ctx, &c.id).await) {
            assert_ne!(
                e.op, IMPACT_CHECKED_OUT,
                "a creation logs no checkout: {e:?}"
            );
        }
    })
    .await;
}
