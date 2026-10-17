//! Port of `changeimpact_test.go`: the change impacts, their reviews and their landing (ADR 0024, 0029, 0075, 0102).
//! `TestChangeImpactsMerge` (the rebase of a change onto a moved target, `SharedNodes`, `settle`) needs the merge and
//! the rebase of slice G3: it is left for G3 (see PORTING.md).

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCheckout, NodeUpdate};
use parking_lot::Mutex;
use serde_json::json;

use crate::tests::draft::{own_change, title};
use crate::tests::helpers::*;
use crate::tests::lifecycle::{new_lifecycle_world, set_title, to_state};
use crate::testsupport::*;
use crate::{Error, ErrorKind, Graph, ItemAuthorizer, ItemPolicy, NewChange, Result};

pub(super) fn plain_change(base: &Baseline, t: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: t.into(),
        baseline_id: base.id.clone(),
        ..Default::default()
    }
}

pub(super) fn modified(pre: &Node, rationale: &str) -> ChangeImpact {
    ChangeImpact {
        intent: INTENT_MODIFIED,
        pre: Some(pre.r#ref()),
        rationale: rationale.into(),
        ..Default::default()
    }
}

pub(super) fn created(key: &str, typ: &str, rationale: &str) -> ChangeImpact {
    ChangeImpact {
        intent: INTENT_CREATED,
        key: key.into(),
        r#type: typ.into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn change_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "PSP v2")).await);
        let pre = f.req.r#ref();
        // an impact: pre + intent + rationale, no post yet
        let got = must(
            propose_or_create(
                g,
                &c.id,
                vec![
                    modified(&f.req, "PSP v2 changes the payment API"),
                    created("TST-2", "TestCase", "cover the new API"),
                ],
            )
            .await,
        );
        assert!(
            got[0].key == "REQ-1"
                && got[0].r#type == "Requirement"
                && got[0].review == REVIEW_PROPOSED
                && got[0].planned(),
            "key / type come from the pre node, review starts proposed: {:?}",
            got[0]
        );
        assert_kind(
            g.propose_impact(&ctx, &c.id, vec![modified(&f.req, "again")])
                .await,
            ErrorKind::Conflict,
            "a node appears once per change",
        );
        assert_kind(
            g.propose_impact(&ctx, &c.id, vec![modified(&f.need, "")])
                .await,
            ErrorKind::Invalid,
            "a rationale is required",
        );
        let ch = must(g.change(&ctx, &c.id).await);
        assert!(
            ch.status == CHANGE_ACTIVE && ch.nodes.len() == 2,
            "change should be active with 2 nodes: {} {}",
            ch.status,
            ch.nodes.len()
        );
        let review = async |s: NodeReview, by: &str, comment: &str| {
            g.impact_node_review(&ctx, &c.id, &got[0].id, s, by, comment)
                .await
        };
        let confirmed = "impact confirmed with the PSP team";
        // accepting needs a comment, and a modification (ADR 0102): a candidate is not accepted
        assert_kind(
            review(REVIEW_ACCEPTED, "alice", "  ").await,
            ErrorKind::Invalid,
            "a review needs a comment",
        );
        assert_kind(
            review(REVIEW_ACCEPTED, "alice", confirmed).await,
            ErrorKind::Invalid,
            "a candidate modifies nothing: not accepted",
        );
        // rejected as it is: to be reworked or withdrawn
        must(review(REVIEW_REJECTED, "bob", "not like this").await);
        assert_kind(
            review(REVIEW_REJECTED, "bob", "no").await,
            ErrorKind::Conflict,
            "a reviewed node cannot be reviewed again",
        );
        // a checkout gives the impact its draft and modifies nothing: the review stands (ADR 0102)
        let checkout = async || {
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: got[0].id.clone(),
                    ..Default::default()
                },
            )
            .await
        };
        let cn = must(checkout().await);
        assert!(
            cn.post.is_some() && !cn.planned() && cn.review == REVIEW_REJECTED,
            "post not set, or the review moved: {cn:?}"
        );
        assert_kind(
            checkout().await,
            ErrorKind::Conflict,
            "a node is checked out once",
        );
        let d = must(draft_of(g, &c.id, "", &got[0].id).await).expect("the draft");
        assert!(
            d.impact == got[0].id
                && d.change == c.id
                && d.base.as_ref() == Some(&pre)
                && cn.post.as_ref().unwrap().is_draft()
                && cn.post.as_ref() == Some(&d.r#ref()),
            "the draft: {d:?}"
        );
        assert_eq!(
            version_count(g, &f.req.id).await,
            1,
            "no version exists while the change works (ADR 0079)"
        );
        assert_kind(
            review(REVIEW_ACCEPTED, "alice", confirmed).await,
            ErrorKind::Conflict,
            "a rejected impact is reviewed again once reworked",
        );
        // reworked: the edit proposes it again
        let cn = must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &got[0].id,
                NodeUpdate {
                    properties: title("Use PSP v2"),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(
            cn.review, REVIEW_PROPOSED,
            "a rejected impact edited is proposed again"
        );
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "landing needs an accepted review",
        );
        must(review(REVIEW_ACCEPTED, "alice", confirmed).await);
        let d = must(draft_of(g, &c.id, "", &got[0].id).await);
        assert!(
            d.is_some() && version_count(g, &f.req.id).await == 1,
            "accepting writes nothing, the draft stays until the change lands: {d:?}"
        );
        let list = must(g.list_change_impacts(&ctx, &c.id).await);
        assert!(
            list.len() == 2
                && list[0].review == REVIEW_ACCEPTED
                && list[0].reviews.len() == 3
                && list[0].reviews[0].by == "bob"
                && list[1].key == "TST-2"
                && !list[1].planned(),
            "unexpected list: {list:?}"
        );
        assert_eq!(list[0].pre.as_ref(), Some(&pre), "pre lost");
    })
    .await;
}

#[tokio::test]
async fn apply_change_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c1 = must(g.create_change(&ctx, own_change(&f.base, "PSP v2")).await);
        let c2 = must(g.create_change(&ctx, own_change(&f.base, "PSP v3")).await);
        let nodes = must(
            propose_or_create(
                g,
                &c1.id,
                vec![
                    modified(&f.req, "PSP v2 changes the API"),
                    created("TST-2", "TestCase", "cover the new API"),
                ],
            )
            .await,
        );
        let other = must(
            g.propose_impact(&ctx, &c2.id, vec![modified(&f.req, "impact of PSP v3")])
                .await,
        );
        // still proposed: the change cannot be applied
        assert_kind(
            apply(g, &c1.id, "").await,
            ErrorKind::Conflict,
            "a change impact awaiting review blocks Apply",
        );
        must(edit(g, &c1.id, &nodes[0].id, set_title("Use PSP v2")).await);
        let req2 = must(
            edit(
                g,
                &c1.id,
                &nodes[0].id,
                Edit {
                    properties: props(&[("owner", "alice".into())]),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            req2.post.as_ref().unwrap().is_draft() && version_count(g, &f.req.id).await == 1,
            "two edits of the draft, no version before the change lands (ADR 0079), got {:?}",
            req2.post
        );
        let tst = must(
            edit(
                g,
                &c1.id,
                &nodes[1].id,
                Edit {
                    properties: title("PSP v2 test"),
                    add_links: vec![LinkWrite {
                        r#type: "verifies".into(),
                        to: req2.post.clone().unwrap(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &c1.id, &nodes[0].id, "alice", "as designed").await);
        must(accept(g, &c1.id, &nodes[1].id, "alice", "needed").await);
        let b = must(apply(g, &c1.id, "").await);
        let head = must(state_of(g, &f.req.id).await);
        assert!(
            head.properties["title"] == "Use PSP v2" && head.properties["owner"] == "alice",
            "both writes must land: {:?}",
            head.properties
        );
        let tid = &tst.post.as_ref().unwrap().id;
        assert!(
            b.nodes.get(&f.req.id) == Some(&head.version)
                && b.nodes.get(tid).is_some_and(|v| *v != 0),
            "baseline {:?} does not hold the landed versions",
            b.nodes
        );
        assert!(
            head.change_id == c1.id
                && head.change_impact == nodes[0].id
                && head.comment == "as designed",
            "landed version must record its origin: {head:?}"
        );
        let list = must(g.list_change_impacts(&ctx, &c1.id).await);
        assert!(
            list[0].landed.as_ref() == Some(&head.r#ref()) && list[1].landed.is_some(),
            "landed not recorded: {list:?}"
        );
        let ch = must(g.change(&ctx, &c1.id).await);
        assert_eq!(ch.status, CHANGE_APPLIED, "change should be applied");
        // the other change's impact (no post yet) follows the new head and is flagged to re-check
        let olist = must(g.list_change_impacts(&ctx, &c2.id).await);
        assert!(
            olist[0].id == other[0].id
                && olist[0].recheck
                && olist[0].pre.as_ref() == Some(&head.r#ref()),
            "impact should move to the new head: {:?}",
            olist[0]
        );
    })
    .await;
}

#[tokio::test]
async fn change_impacts_lifecycle() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let g = &w.g;
        let c = must(
            g.create_change(&ctx, own_change(&w.base, "edit REQ-1"))
                .await,
        );
        let nodes = must(
            propose_or_create(
                g,
                &c.id,
                vec![
                    modified(&w.req1, "reword"),
                    modified(&w.req2, "approve without title"),
                ],
            )
            .await,
        );
        let review = async |id: &ChangeImpactID| must(accept(g, &c.id, id, "alice", "ok").await);
        // a node is edited in any state while it is in a change (ADR 0078)
        must(edit(g, &c.id, &nodes[0].id, set_title("one v2")).await);
        assert_kind(
            edit(g, &c.id, &nodes[0].id, to_state("released")).await,
            ErrorKind::Invalid,
            "no such transition",
        );
        for e in [to_state("draft"), set_title("one v2")] {
            must(edit(g, &c.id, &nodes[0].id, e).await);
        }
        // req2 goes proposed → draft, and cannot be approved without the title the approval requires
        must(edit(g, &c.id, &nodes[1].id, to_state("draft")).await);
        assert_err(
            edit(g, &c.id, &nodes[1].id, to_state("approved")).await,
            ErrorKind::Invalid,
            &[r#"attribute "title""#],
            "a transition that lacks its required attribute",
        );
        review(&nodes[0].id).await;
        review(&nodes[1].id).await;
        assert_err(
            apply(g, &c.id, "").await,
            ErrorKind::Invalid,
            &["cannot land"],
            "apply must refuse a node left in a state that cannot land",
        );
        must(edit(g, &c.id, &nodes[0].id, to_state("approved")).await);
        must(edit(g, &c.id, &nodes[1].id, set_title("two")).await);
        must(edit(g, &c.id, &nodes[1].id, to_state("approved")).await);
        // the commit of the first apply wrote the versions on the change's branch, the merge refused them; the edit
        // since sends the review back to proposed (ADR 0102), the moves do not
        review(&nodes[1].id).await;
        must(apply(g, &c.id, "").await);
        let n1 = must(state_of(g, &w.req1.id).await);
        assert!(
            n1.state == "approved" && n1.properties["title"] == "one v2" && n1.change_id == c.id,
            "REQ-1 after apply: {n1:?}"
        );
        let n2 = must(state_of(g, &w.req2.id).await);
        assert!(
            n2.state == "approved" && n2.properties["title"] == "two",
            "REQ-2 after apply: {n2:?}"
        );
    })
    .await;
}

// ImpactsOf scopes a change's impacts to one execution: the context graph (Pre) and modified graph (Post) of
// exactly what that run touched, not the whole change.
#[tokio::test]
async fn impacts_of() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "PSP v2")).await);
        let added = must(
            propose_or_create(
                g,
                &c.id,
                vec![
                    ChangeImpact {
                        execution: "run-a".into(),
                        ..modified(&f.req, "touched by run A")
                    },
                    ChangeImpact {
                        execution: "run-b".into(),
                        ..modified(&f.need, "touched by run B")
                    },
                ],
            )
            .await,
        );
        for (i, (run, t)) in [("run-a", "A"), ("run-b", "B")].iter().enumerate() {
            must(
                edit(
                    g,
                    &c.id,
                    &added[i].id,
                    Edit {
                        execution: run.to_string(),
                        properties: title(t),
                        ..Default::default()
                    },
                )
                .await,
            );
        }
        let a = must(g.impacts_of(&ctx, &c.id, "run-a").await);
        assert!(
            a.len() == 1 && a[0].key == "REQ-1" && a[0].post.is_some(),
            "run-a impacts: {a:?}"
        );
        let b = must(g.impacts_of(&ctx, &c.id, "run-b").await);
        assert!(
            b.len() == 1 && b[0].key == "NEED-1" && b[0].post.is_some(),
            "run-b impacts: {b:?}"
        );
        let none = must(g.impacts_of(&ctx, &c.id, "run-c").await);
        assert!(none.is_empty(), "unknown execution: {none:?}");
    })
    .await;
}

struct RefuseReviewer(&'static str);

impl ReviewPolicy for RefuseReviewer {
    fn review(&self, q: &ReviewRequest) -> std::result::Result<(), goap_domain::Error> {
        if q.reviewer == self.0 {
            return Err(goap_domain::Error::Invalid("no".into()));
        }
        Ok(())
    }
}

async fn proposed_by_bob(f: &Fixture, c: &Change) -> ChangeImpact {
    let got = must(
        f.g.propose_impact(
            &ctx(),
            &c.id,
            vec![ChangeImpact {
                produced_by: "bob".into(),
                ..modified(&f.req, "r")
            }],
        )
        .await,
    );
    touch(&f.g, &c.id, &got[0].id).await;
    got[0].clone()
}

// A ReviewPolicy refuses a review before anything is written; unset, anyone reviews (ADR 0075).
#[tokio::test]
async fn review_policy() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        let c = must(f.g.create_change(&ctx, plain_change(&f.base, "t")).await);
        let got = proposed_by_bob(&f, &c).await;
        let log = async |g: &Graph| {
            must(
                g.change_log(
                    &ctx,
                    &LogFilter {
                        change: c.id.clone(),
                        ..Default::default()
                    },
                )
                .await,
            )
            .0
            .len()
        };
        let before = log(&f.g).await;
        f.g.review_policy = Some(Arc::new(RefuseReviewer("bob")));
        let g = &f.g;
        assert_err(
            accept(g, &c.id, &got.id, "bob", "mine").await,
            ErrorKind::Invalid,
            &["refused"],
            "the policy refuses",
        );
        let ch = must(g.change(&ctx, &c.id).await);
        assert!(
            ch.nodes[0].review == REVIEW_PROPOSED
                && ch.nodes[0].reviews.is_empty()
                && log(g).await == before,
            "a refusal writes nothing: {:?}",
            ch.nodes[0]
        );
        must(accept(g, &c.id, &got.id, "alice", "ok").await);
    })
    .await;
}

#[tokio::test]
async fn nil_review_policy_reviews_as_before() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let c = must(f.g.create_change(&ctx, plain_change(&f.base, "t")).await);
        let got = proposed_by_bob(&f, &c).await;
        assert!(f.g.review_policy.is_none(), "unset by default");
        must(accept(&f.g, &c.id, &got.id, "bob", "mine").await);
    })
    .await;
}

// ReopenImpacts sends accepted impacts back to proposed, as a review event; the others are left alone (ADR 0075).
#[tokio::test]
async fn reopen_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(g.create_change(&ctx, plain_change(&f.base, "t")).await);
        let id = proposed_by_bob(&f, &c).await.id;
        let done = must(
            g.reopen_impacts(&ctx, &c.id, std::slice::from_ref(&id), "why")
                .await,
        );
        assert!(done.is_empty(), "a proposed impact is left alone: {done:?}");
        assert_kind(
            g.reopen_impacts(&ctx, &c.id, std::slice::from_ref(&id), " ")
                .await,
            ErrorKind::Invalid,
            "a comment is mandatory",
        );
        must(accept(g, &c.id, &id, "alice", "ok").await);
        let done = must(
            g.reopen_impacts(&ctx, &c.id, std::slice::from_ref(&id), "derogation expired")
                .await,
        );
        assert_eq!(done.len(), 1, "reopen");
        let ch = must(g.change(&ctx, &c.id).await);
        assert!(
            ch.nodes[0].review == REVIEW_PROPOSED && ch.nodes[0].reviews.len() == 2,
            "back to proposed, history kept: {:?}",
            ch.nodes[0]
        );
        must(accept(g, &c.id, &id, "carol", "again").await);
        assert_kind(
            g.reopen_impacts(&ctx, &c.id, &[ChangeImpactID::new("nope")], "why")
                .await,
            ErrorKind::NotFound,
            "unknown impact",
        );
    })
    .await;
}

struct RecordingAuthorizer {
    change: ChangeID,
    asked: Arc<Mutex<Vec<String>>>,
}

#[async_trait]
impl ItemAuthorizer for RecordingAuthorizer {
    async fn authorize(
        &self,
        _: &Ctx,
        ch: &Change,
        it: &ChangeItem,
        p: &ItemPermission,
    ) -> Result<()> {
        self.asked
            .lock()
            .push(format!("{}/{}", p.permission, p.subject_field));
        assert_eq!(ch.id, self.change, "the change of the item");
        if it.data.get("who").and_then(|v| v.as_str()) == Some("mallory") {
            return Err(Error::msg("refused"));
        }
        Ok(())
    }
}

// ItemAuthorizer is asked about the items of a kind that asks a permission of its writer, and only those (ADR 0075).
#[tokio::test]
async fn item_authorizer() {
    let kind = ItemType::new("testSigned");
    require_item_permission(
        kind.clone(),
        ItemPermission {
            permission: "thing:sign".into(),
            subject_field: "who".into(),
        },
    );
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        let mut d = goap_def::Domain {
            name: "itemtest".into(),
            version: "1.0.0".into(),
            ..Default::default()
        };
        d.schema.item_types.push(goap_def::ItemType {
            name: kind.to_string(),
            ..Default::default()
        });
        use_types(
            &mut f.g,
            Arc::new(goap_typecat::new(&[d]).expect("catalogue")),
        );
        let c = must(f.g.create_change(&ctx, plain_change(&f.base, "t")).await);
        let asked = Arc::new(Mutex::new(Vec::new()));
        f.g.item_authorizer = Some(Arc::new(RecordingAuthorizer {
            change: c.id.clone(),
            asked: asked.clone(),
        }));
        let g = &f.g;
        let item = |t: ItemType, target: &str, data: serde_json::Value| ChangeItem {
            r#type: t,
            target: target.into(),
            data: data.as_object().cloned().unwrap_or_default(),
            ..Default::default()
        };
        must(
            g.add_items(
                &ctx,
                &c.id,
                vec![item(TYPE_ARTIFACT, "note", json!({"text": "x"}))],
            )
            .await,
        );
        assert!(asked.lock().is_empty(), "an ordinary item asks nothing");
        let err = g
            .add_items(
                &ctx,
                &c.id,
                vec![item(kind.clone(), "", json!({"who": "mallory"}))],
            )
            .await
            .unwrap_err();
        assert!(err.to_string().contains("refused"), "refused: {err}");
        let ch = must(g.change(&ctx, &c.id).await);
        assert!(
            ch.items_of_type(&kind).is_empty(),
            "a refused item is not stored"
        );
        must(
            g.add_items(
                &ctx,
                &c.id,
                vec![item(kind.clone(), "", json!({"who": "alice"}))],
            )
            .await,
        );
        let a = asked.lock().clone();
        assert!(
            a.len() == 2 && a[1] == "thing:sign/who",
            "authorized: {a:?}"
        );
    })
    .await;
}

struct HighPolicy;

#[async_trait]
impl ItemPolicy for HighPolicy {
    async fn check(&self, _: &Ctx, ch: &Change, it: &ChangeItem) -> Result<()> {
        if ch.data.get("level").and_then(|v| v.as_str()) == Some("high")
            && it.data.get("text").and_then(|v| v.as_str()) == Some("no")
        {
            return Err(Error::msg("not on a high change"));
        }
        Ok(())
    }
}

// ItemPolicy is asked about every item AddItems stores, and an error refuses the write (ADR 0075 §3).
#[tokio::test]
async fn item_policy() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut f = new_fixture(r).await;
        let mut data = serde_json::Map::new();
        data.insert("level".into(), "high".into());
        let c = must(
            f.g.create_change(
                &ctx,
                NewChange {
                    data,
                    ..plain_change(&f.base, "t")
                },
            )
            .await,
        );
        f.g.item_policy = Some(Arc::new(HighPolicy));
        let g = &f.g;
        let item = |text: &str| ChangeItem {
            r#type: TYPE_ARTIFACT,
            target: "note".into(),
            data: json!({"text": text}).as_object().cloned().unwrap(),
            ..Default::default()
        };
        must(g.add_items(&ctx, &c.id, vec![item("yes")]).await);
        assert_err(
            g.add_items(&ctx, &c.id, vec![item("no")]).await,
            ErrorKind::Invalid,
            &["not on a high change"],
            "refused",
        );
        let ch = must(g.change(&ctx, &c.id).await);
        assert_eq!(ch.items.len(), 1, "a refused item is not stored");
    })
    .await;
}
