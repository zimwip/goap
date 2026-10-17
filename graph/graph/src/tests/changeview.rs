//! A change is seen at three levels of its change impacts (ADR 0032): written,
//! accepted, landed; a flow sees its own drafts.

use goap_domain::*;
use serde_json::Value;

use crate::changeview::{VIEW_ACCEPTED, VIEW_LANDED, VIEW_WRITTEN};
use crate::tests::draft::title;
use crate::tests::flownodes::new_flow_world;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

fn retitle(t: &str) -> Edit {
    Edit {
        properties: title(t),
        ..Default::default()
    }
}

#[tokio::test]
async fn change_view_levels() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "PSP v2".into(),
                    baseline_id: f.base.id.clone(),
                    own_branch: true,
                    ..Default::default()
                },
            )
            .await,
        );
        let (req, need) = (f.req.r#ref(), f.need.r#ref());
        let modified = |p: NodeRef, rationale: &str| ChangeImpact {
            intent: INTENT_MODIFIED,
            pre: Some(p),
            rationale: rationale.into(),
            ..Default::default()
        };
        let added = must(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![
                    modified(req.clone(), "new PSP"),
                    modified(need.clone(), "reword"),
                    modified(f.test.r#ref(), "obsolete"),
                ],
            )
            .await,
        );
        let req_w = must(edit(g, &c.id, &added[0].id, retitle("Use PSP v2")).await);
        let need_w = must(edit(g, &c.id, &added[1].id, retitle("Pay")).await);
        let test_w = must(edit(g, &c.id, &added[2].id, retitle("Obsolete")).await);
        must(accept(g, &c.id, &req_w.id, "u", "ok").await);
        must(accept(g, &c.id, &added[2].id, "u", "ok").await);
        let post = |cn: &ChangeImpact| cn.post.clone().expect("post");

        let view = async |level: &str| must(g.change_view(&ctx, &c.id, "", level).await);
        // written: everything proposed or accepted
        let w = view(VIEW_WRITTEN).await;
        assert!(
            w.contains(&post(&req_w)) && w.contains(&post(&need_w)),
            "written view: {:?}",
            w.nodes
        );
        assert!(
            w.contains(&post(&test_w)),
            "the written view holds every version written: {:?}",
            w.nodes
        );
        // accepted: NEED-1 awaits its review, it stays as released
        let a = view(VIEW_ACCEPTED).await;
        assert!(
            a.contains(&post(&req_w)) && a.contains(&need),
            "accepted view: {:?}",
            a.nodes
        );
        // landed: nothing yet
        assert_eq!(
            view(VIEW_LANDED).await.id,
            f.base.id,
            "landed view before apply"
        );
        // a rejected version leaves the written view
        must(
            g.impact_node_review(&ctx, &c.id, &need_w.id, REVIEW_REJECTED, "u", "no")
                .await,
        );
        let w = view(VIEW_WRITTEN).await;
        assert!(
            w.contains(&need),
            "written view after the rejection: {:?}",
            w.nodes
        );
        // rejected: reworked or withdrawn before the change merges (ADR 0102)
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "a rejected impact holds the merge",
        );
        must(g.withdraw_impact(&ctx, &c.id, &need_w.id, "", "").await);
        let res = must(apply(g, &c.id, "").await);
        let l = view(VIEW_LANDED).await;
        assert!(
            l.id == res.id
                && l.nodes.get(&f.req.id) == Some(&(f.req.version + 1))
                && l.contains(&need),
            "landed view: {l:?}"
        );
        assert!(
            g.change_view(&ctx, &c.id, "", "someday").await.is_err(),
            "unknown level"
        );
    })
    .await;
}

// A flow sees its own versions, the main flow its own (ADR 0032: an option is a branch seen before it lands).
#[tokio::test]
async fn change_view_of_a_flow() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        let pre = w.f.req.r#ref();
        let added = must(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(pre.clone()),
                    rationale: "second look".into(),
                    flow: w.flow.clone(),
                    execution: "e3".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(
            edit(
                g,
                &c.id,
                &added[0].id,
                Edit {
                    flow: w.flow.clone(),
                    execution: "e3".into(),
                    properties: title("B"),
                    ..Default::default()
                },
            )
            .await,
        );
        let flow_view = must(g.change_view(&ctx, &c.id, &w.flow, VIEW_WRITTEN).await);
        let main_view = must(g.change_view(&ctx, &c.id, "", VIEW_WRITTEN).await);
        let fv = must(
            g.change_node(
                &ctx,
                &c.id,
                &w.flow,
                &NodeRef {
                    id: pre.id.clone(),
                    version: flow_view.nodes.get(&pre.id).copied().unwrap_or_default(),
                },
            )
            .await,
        );
        assert!(
            fv.properties.get("title") == Some(&Value::from("B"))
                && main_view.contains(w.req.post.as_ref().expect("post")),
            "flow view {:?}, main view {:?}",
            flow_view.nodes,
            main_view.nodes
        );
        // the flow relaunched the step that created TST-2: it does not see it; the main flow does
        let tst = w.tst.post.clone().expect("post");
        assert!(
            !flow_view.nodes.contains_key(&tst.id) && main_view.contains(&tst),
            "TST-2: flow {:?}, main {:?}",
            flow_view.nodes,
            main_view.nodes
        );
        // accepted: nothing of the flow is reviewed yet
        let a = must(g.change_view(&ctx, &c.id, &w.flow, VIEW_ACCEPTED).await);
        assert!(a.contains(&pre), "flow accepted view: {:?}", a.nodes);
    })
    .await;
}
