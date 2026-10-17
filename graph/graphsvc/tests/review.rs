mod common;

use std::sync::Arc;

use common::{as_, casbin, code, floor, graph};
use goap_connect::{Code, ConnectError};
use goap_ctx::Ctx;
use goap_domain::{
    ChangeImpact, ChangeImpactID, INTENT_MODIFIED, MAIN_BRANCH, REVIEW_ACCEPTED, REVIEW_PROPOSED,
};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{NewChange, NodeCheckout, NodeUpdate};
use goap_graphsvc::Handler;
use goap_pbconv as pbconv;

/// The review object over the RPCs (ADR 0080): open, update, submit, discard; the author or an administrator only;
/// the reviewer is the principal of the request; a refusal of the submit leaves the review open.
#[tokio::test]
async fn review_rpcs() {
    let ctx = &Ctx::background();
    let mut g = graph();
    g.caller = Some(Arc::new(goap_graphsvc::caller));
    let g = Arc::new(g);
    let h = Handler {
        authz: Some(casbin()),
        floor: Some(floor()),
        ..Handler::new(g.clone())
    };
    let import = async |key: &str, typ: &str, title: &str| {
        Box::pin(goap_graphtest::import(
            ctx,
            &g,
            goap_graphtest::Node {
                key: key.into(),
                r#type: typ.into(),
                properties: obj! {"title" => title},
                ..Default::default()
            },
        ))
        .await
        .unwrap()
    };
    let req1 = import("REQ-1", "Requirement", "one").await;
    let need = import("NEED-1", "Need", "n").await;
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                baseline_id: base.id,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let cid = c.id.to_string();
    let mut imps: Vec<String> = Vec::new();
    for n in [&req1, &need] {
        let out = g
            .propose_impact(
                ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(n.r#ref()),
                    rationale: "r".into(),
                    ..Default::default()
                }],
            )
            .await
            .unwrap();
        // each impact modifies its node: only a modification is accepted (ADR 0102)
        g.impact_node_checkout(
            ctx,
            &c.id,
            NodeCheckout {
                impact: out[0].id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
        g.impact_node_update(
            ctx,
            &c.id,
            &out[0].id,
            NodeUpdate {
                properties: obj! {"title" => "changed"},
                ..Default::default()
            },
        )
        .await
        .unwrap();
        imps.push(out[0].id.to_string());
    }
    let open = as_(
        pb::ReviewOpenRequest {
            change_id: cid.clone(),
            comment: "global".into(),
            ..Default::default()
        },
        "alice",
        "contributor",
    );
    let o = h.review_open(ctx, open).await;
    let rec = match o {
        Ok(o)
            if o.msg
                .review
                .as_ref()
                .is_some_and(|r| r.status == "open" && r.by == "alice") =>
        {
            o.msg.review.unwrap()
        }
        other => panic!("open: {other:?}"),
    };
    let key = rec.key.clone();
    let upd = async |subject: &str,
                     roles: &str,
                     mut m: pb::ReviewUpdateRequest|
           -> Result<pb::ReviewRecord, ConnectError> {
        m.change_id = cid.clone();
        m.key = key.clone();
        let out = h.review_update(ctx, as_(m, subject, roles)).await?;
        Ok(out.msg.review.unwrap())
    };
    let rec = upd(
        "alice",
        "contributor",
        pb::ReviewUpdateRequest {
            add: imps.clone(),
            entries: vec![
                pb::ReviewEntryEdit {
                    change_impact_id: imps[0].clone(),
                    set_outcome: true,
                    outcome: "accept".into(),
                    set_comment: true,
                    comment: "good".into(),
                },
                pb::ReviewEntryEdit {
                    change_impact_id: imps[1].clone(),
                    set_outcome: true,
                    outcome: "reject".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        },
    )
    .await;
    assert!(
        matches!(&rec, Ok(r) if r.entries.len() == 2 && r.entries[0].outcome == "accept" && r.entries[0].comment == "good"),
        "update: {rec:?}"
    );
    let comment = |c: &str| pb::ReviewUpdateRequest {
        set_comment: true,
        comment: c.into(),
        ..Default::default()
    };
    let r = upd("mallory", "contributor", comment("x")).await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "someone else: {r:?}"
    );
    upd("root", "admin", comment("global (edited)"))
        .await
        .expect("an administrator");
    let r = upd(
        "alice",
        "contributor",
        pb::ReviewUpdateRequest {
            add: vec!["nope".into()],
            ..Default::default()
        },
    )
    .await;
    assert_eq!(code(&r), Some(Code::NotFound), "unknown impact: {r:?}");
    // a review made elsewhere meanwhile: the submit is refused whole, the review stays open, the error names the
    // entry
    Box::pin(g.impact_node_review_on(
        ctx,
        &c.id,
        "",
        "",
        &ChangeImpactID::from(imps[1].as_str()),
        REVIEW_ACCEPTED,
        "carol",
        "meanwhile",
    ))
    .await
    .unwrap();
    let submit = || {
        as_(
            pb::ReviewSubmitRequest {
                change_id: cid.clone(),
                key: key.clone(),
            },
            "alice",
            "contributor",
        )
    };
    let r = Box::pin(h.review_submit(ctx, submit())).await;
    assert_eq!(
        code(&r),
        Some(Code::FailedPrecondition),
        "a refused submit: {r:?}"
    );
    let ch = g.change(ctx, &c.id).await.unwrap();
    assert_eq!(
        ch.nodes[0].review, REVIEW_PROPOSED,
        "all or none: {:?}",
        ch.nodes[0]
    );
    upd(
        "alice",
        "contributor",
        pb::ReviewUpdateRequest {
            remove: vec![imps[1].clone()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let out = Box::pin(h.review_submit(ctx, submit())).await;
    assert!(
        matches!(&out, Ok(o) if o.msg.review.as_ref().is_some_and(|r| r.status == "submitted" && r.submitted_at.is_some())),
        "submit: {out:?}"
    );
    let ch = g.change(ctx, &c.id).await.unwrap();
    let r = &ch.nodes[0].reviews[0];
    assert!(
        ch.nodes[0].review == REVIEW_ACCEPTED && r.review_id == key && r.by == "alice",
        "the impact is reviewed by the submitter in the review: {:?}",
        ch.nodes[0]
    );
    let got = pbconv::change_impact_to_pb(&ch.nodes[0]).reviews[0]
        .review_id
        .clone();
    assert_eq!(got, key, "review id on the wire");
    let r = upd("alice", "contributor", comment("late")).await;
    assert_eq!(code(&r), Some(Code::FailedPrecondition), "final: {r:?}");
    let dis = as_(
        pb::ReviewDiscardRequest {
            change_id: cid.clone(),
            key: key.clone(),
        },
        "alice",
        "contributor",
    );
    let r = h.review_discard(ctx, dis).await;
    assert_eq!(
        code(&r),
        Some(Code::FailedPrecondition),
        "a submitted review is not discarded: {r:?}"
    );
    // the batch RPC under it reviews as the principal of the request, and anonymous callers are refused upstream
    let bt = as_(
        pb::ImpactNodeReviewBatchRequest {
            change_id: cid.clone(),
            review_id: "ad-hoc".into(),
            verdicts: vec![pb::ImpactVerdict {
                change_impact_id: imps[1].clone(),
                accept: false,
                comment: "no".into(),
            }],
            ..Default::default()
        },
        "bob",
        "contributor",
    );
    let r = Box::pin(h.impact_node_review_batch(ctx, bt)).await;
    assert_eq!(
        code(&r),
        Some(Code::FailedPrecondition),
        "batch on a reviewed impact: {r:?}"
    );
}
