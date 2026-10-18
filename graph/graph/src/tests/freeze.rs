//! A node has no version while the change works (ADR 0079): the review, the edits and the
//! transitions are on its draft, and landing writes the one version.

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{LinkWrite, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate};
use serde_json::Value;

use crate::tests::algorithms::algo_types;
use crate::tests::draft::title;
use crate::tests::events::RecSink;
use crate::tests::helpers::*;
use crate::tests::lifecycle::{new_lifecycle_world, new_lifecycle_world_g};
use crate::testsupport::*;
use crate::{Error, ErrorKind, Graph, GraphEvent, NewChange, Result, TransitionAuthorizer};

/// Go's algoWorld: Req (property "code" must match ^REQ-[0-9]+$, approve stamps the node), Doc (release needs its
/// requirements approved).
pub(crate) struct AlgoWorld {
    pub(crate) g: Graph,
    pub(crate) base: Baseline,
}

pub(crate) async fn new_algo_world(r: &TestRepo) -> AlgoWorld {
    let mut g = r.graph();
    let (types, _, _) = algo_types();
    use_types(&mut g, Arc::new(types));
    let req = must(
        seed_node(
            &g,
            NewNode {
                key: "R1".into(),
                r#type: "Req".into(),
                properties: props(&[("code", Value::from("REQ-1"))]),
                state: "draft".into(),
                ..Default::default()
            },
        )
        .await,
    );
    let doc = must(
        seed_node(
            &g,
            NewNode {
                key: "D1".into(),
                r#type: "Doc".into(),
                state: "draft".into(),
                links: vec![(LINK_CONTAINS.into(), req.r#ref())],
                ..Default::default()
            },
        )
        .await,
    );
    let base = must(pin_baseline(&g, "", &[req.r#ref(), doc.r#ref()]).await);
    AlgoWorld { g, base }
}

impl AlgoWorld {
    pub(crate) async fn change(&self) -> Change {
        must(
            self.g
                .create_change(
                    &ctx(),
                    NewChange {
                        project_id: "PROJ-ROOT".into(),
                        title: "chg".into(),
                        baseline_id: self.base.id.clone(),
                        ..Default::default()
                    },
                )
                .await,
        )
    }
}

fn state(s: &str) -> Edit {
    Edit {
        state: s.into(),
        ..Default::default()
    }
}

// A node has no version while the change works (ADR 0079): its draft stays until the change lands. An accepted review
// is gated by what a version must satisfy, checked on the draft, and writes nothing.
#[tokio::test]
async fn accept_runs_validators_on_the_draft() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_algo_world(r).await;
        let g = &w.g;
        let c = w.change().await;
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "R7".into(),
                    r#type: "Req".into(),
                    properties: props(&[("code", Value::from("nope"))]),
                    rationale: "new".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let post = cn.post.clone().expect("post");
        // the property validator judges the version when it is accepted
        assert_err(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
            ErrorKind::Invalid,
            &["must match"],
            "a failing validator refuses the accept",
        );
        let n = must(g.change_node(&ctx, &c.id, "", &post).await);
        assert!(
            n.is_draft() && n.properties.get("code") == Some(&Value::from("nope")),
            "the draft is unchanged: {n:?}"
        );
        let seen = must(seen_impact(g, &c.id, &cn.id, "").await);
        assert!(
            seen.review == REVIEW_PROPOSED && seen.reviews.is_empty(),
            "the review stays proposed, and nothing is recorded: {seen:?}"
        );
        must(
            g.impact_node_update(
                &ctx,
                &c.id,
                &cn.id,
                NodeUpdate {
                    properties: props(&[("code", Value::from("REQ-77"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
        );
        assert_eq!(
            version_count(g, &post.id).await,
            0,
            "the accept wrote no version"
        );
        must(edit(g, &c.id, &cn.id, state("approved")).await);
        // landing writes the version from the draft
        must(apply(g, &c.id, "").await);
        let vs = must(g.versions(&ctx, &post.id).await);
        assert!(
            vs.len() == 1
                && vs[0].version == 1
                && vs[0].state == "approved"
                && vs[0].properties.get("code") == Some(&Value::from("REQ-77")),
            "landing wrote the one version: {vs:?}"
        );
    })
    .await;
}

// The flow of a user (ADR 0077, 0079): propose an existing node, check it out, update, accept, update again, accept,
// land: one draft, no version until landing, the review sent back to proposed in between, one version at the end.
#[tokio::test]
async fn edit_after_accept_is_on_the_draft() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "edit".into(),
                    baseline_id: f.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let pre = f.req.r#ref();
        let prop = must(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(pre.clone()),
                    rationale: "look".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        let co = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: prop[0].id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let upd = |t: &str| NodeUpdate {
            properties: title(t),
            ..Default::default()
        };
        must(g.impact_node_update(&ctx, &c.id, &co.id, upd("one")).await);
        must(
            g.impact_node_review(&ctx, &c.id, &co.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
        );
        assert_kind(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    impact: co.id.clone(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "an impact holding a draft is not checked out again",
        );
        let up = must(g.impact_node_update(&ctx, &c.id, &co.id, upd("two")).await);
        assert!(
            up.review == REVIEW_PROPOSED && up.post == co.post,
            "the update is on the draft and sends the review back: {up:?}"
        );
        // a link edit does the same
        must(
            g.impact_node_review(&ctx, &c.id, &co.id, REVIEW_ACCEPTED, "bob", "ok again")
                .await,
        );
        must(
            g.impact_link_create(
                &ctx,
                &c.id,
                &co.id,
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
        must(accept(g, &c.id, &co.id, "bob", "and again").await);

        let got: Vec<(ImpactOp, NodeReview)> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .map(|e| (e.op, e.review.map(|r| r.status).unwrap_or_default()))
            .collect();
        let none = NodeReview::default();
        let want = vec![
            (IMPACT_PROPOSED, none.clone()),
            (IMPACT_CHECKED_OUT, none.clone()),
            (IMPACT_UPDATED, none.clone()),
            (IMPACT_REVIEWED, REVIEW_ACCEPTED),
            (IMPACT_UPDATED, none.clone()),
            (IMPACT_REVIEWED, REVIEW_PROPOSED),
            (IMPACT_REVIEWED, REVIEW_ACCEPTED),
            (IMPACT_UPDATED, none.clone()),
            (IMPACT_REVIEWED, REVIEW_PROPOSED),
            (IMPACT_REVIEWED, REVIEW_ACCEPTED),
        ];
        assert_eq!(got, want, "log");
        assert_eq!(
            version_count(g, &f.req.id).await,
            pre.version as usize,
            "no version written during the change"
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
            landed.version == pre.version + 1
                && landed.properties.get("title") == Some(&Value::from("two")),
            "landing writes the version from the draft: {landed:?}"
        );
    })
    .await;
}

/// The authorizer of the in-place transition test: refuses the first start of REQ-20.
struct FirstStartDenied(parking_lot::Mutex<Vec<String>>);

#[async_trait]
impl TransitionAuthorizer for FirstStartDenied {
    async fn authorize(&self, _: &Ctx, n: &Node, t: &Transition) -> Result<()> {
        let mut asked = self.0.lock();
        asked.push(t.name.clone());
        if t.name == "start" && n.key == "REQ-20" && asked.len() == 1 {
            return Err(Error::msg("no permission"));
        }
        Ok(())
    }
}

// A transition sets the state of the draft of the node (ADR 0079): no version, one transitioned event with the patch;
// permission and guard are checked when it is taken, and the guard sees the change, the impact and the draft.
#[tokio::test]
async fn transition_on_the_draft() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_lifecycle_world_g(
            r,
            r#"impact.review == "accepted" && draft.new && draft.key == "REQ-20" && draft.state == "approved" && draft.props.title == "twenty" && change.title == "create" && node.state == "approved""#,
        )
        .await;
        w.g.authorizer = Some(Arc::new(FirstStartDenied(Default::default())));
        let g = &w.g;
        let c = w.change("create").await;
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-20".into(),
                    r#type: "Requirement".into(),
                    properties: title("twenty"),
                    rationale: "new".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let post = cn.post.clone().expect("post");
        let mv = async |to: &str| {
            g.impact_node_transition(
                &ctx,
                &c.id,
                NodeTransition {
                    checkout: NodeCheckout {
                        impact: cn.id.clone(),
                        ..Default::default()
                    },
                    to: to.into(),
                },
            )
            .await
        };
        let err = mv("draft").await.expect_err("refused");
        assert!(
            err.to_string().contains("no permission"),
            "the authorizer is asked before an in-place transition: {err}"
        );
        let n = must(g.change_node(&ctx, &c.id, "", &post).await);
        assert_eq!(n.state, "proposed", "a refused transition changes nothing");
        must(mv("draft").await);
        // the guard sees the impact: its review is not accepted yet
        assert_err(
            mv("approved").await,
            ErrorKind::Invalid,
            &["guard not satisfied"],
            "the guard is checked in place",
        );
        let n = must(g.change_node(&ctx, &c.id, "", &post).await);
        assert!(
            n.state == "draft" && version_count(g, &post.id).await == 0,
            "one draft, in the new state, and no version: {n:?}"
        );
        let tr: Vec<ImpactEvent> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .filter(|e| e.op == IMPACT_TRANSITIONED)
            .collect();
        assert!(
            tr.len() == 1 && tr[0].post.as_ref() == Some(&post) && !tr[0].starts_draft(),
            "one transitioned event, on the draft: {tr:?}"
        );
        let st = tr[0].patch.get("state").cloned().unwrap_or_default();
        assert!(
            st.get("from") == Some(&Value::from("proposed"))
                && st.get("to") == Some(&Value::from("draft")),
            "patch: {:?}",
            tr[0].patch
        );
        // accepted: the guard passes, on the draft, and the acceptance stands
        must(
            g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "bob", "ok")
                .await,
        );
        must(mv("approved").await);
        let n = must(g.change_node(&ctx, &c.id, "", &post).await);
        assert!(
            n.state == "approved" && version_count(g, &post.id).await == 0,
            "still one draft, no version: {n:?}"
        );
        let seen = must(seen_impact(g, &c.id, &cn.id, "").await);
        assert_eq!(
            seen.review, REVIEW_ACCEPTED,
            "a transition does not undo the acceptance its guard read"
        );
        must(apply(g, &c.id, "").await);
        let n = must(g.node_by_key(&ctx, "", "REQ-20").await);
        assert_eq!(n.state, "approved", "landed");
    })
    .await;
}

// A creation then its transition in one change is one version, in the final state (ADR 0079): the transition does
// not change the phase of the impact, and an impact written in a state the change left stays frozen (ADR 0058).
#[tokio::test]
async fn create_then_transition_is_one_version() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let g = &w.g;
        let c = w.change("create and approve").await;
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "REQ-30".into(),
                    r#type: "Requirement".into(),
                    properties: title("thirty"),
                    rationale: "new".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        for to in ["draft", "approved"] {
            must(edit(g, &c.id, &cn.id, state(to)).await);
        }
        must(accept(g, &c.id, &cn.id, "bob", "ok").await);
        must(apply(g, &c.id, "").await);
        let n = must(g.node_by_key(&ctx, "", "REQ-30").await);
        let vs = must(g.versions(&ctx, &n.id).await);
        assert!(
            vs.len() == 1 && vs[0].version == 1 && vs[0].state == "approved",
            "one version in the final state: {vs:?}"
        );
        let ops: Vec<ImpactOp> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .map(|e| e.op)
            .collect();
        let want = [
            IMPACT_CREATED,
            IMPACT_TRANSITIONED,
            IMPACT_TRANSITIONED,
            IMPACT_REVIEWED,
            IMPACT_LANDED,
        ];
        assert!(
            ops.len() >= want.len() && ops[..want.len()] == want,
            "log {ops:?}, want {want:?} then landed events"
        );
    })
    .await;
}

// A transition of a node that has no draft in the change checks it out first (ADR 0079): events checkedOut then
// transitioned, no version; the impact can be withdrawn, and a cancelled checkout goes back to the impact without
// draft.
#[tokio::test]
async fn transition_checks_the_node_out() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_lifecycle_world(r).await;
        let g = &w.g;
        let c = w.change("reopen").await;
        let imp = w.declare(&c, &w.req1).await; // REQ-1 is approved in the baseline
        assert_kind(
            accept(g, &c.id, &imp, "bob", "confirmed").await,
            ErrorKind::Invalid,
            "a candidate is not accepted (ADR 0102)",
        );
        let moved = must(edit(g, &c.id, &imp, state("draft")).await);
        assert!(
            moved.post.as_ref().is_some_and(|p| p.is_draft()) && moved.review == REVIEW_PROPOSED,
            "the move checked the node out, a modification to review: {moved:?}"
        );
        assert_eq!(
            version_count(g, &w.req1.id).await,
            w.req1.version as usize,
            "no version written"
        );
        let n = must(
            g.change_node(&ctx, &c.id, "", moved.post.as_ref().expect("post"))
                .await,
        );
        assert_eq!(n.state, "draft", "the draft is in the new state");
        let ops: Vec<ImpactOp> = must(g.change_events(&ctx, &c.id).await)
            .into_iter()
            .map(|e| e.op)
            .collect();
        assert_eq!(
            ops,
            vec![IMPACT_PROPOSED, IMPACT_CHECKED_OUT, IMPACT_TRANSITIONED],
            "log"
        );
        let back = must(g.impact_node_cancel(&ctx, &c.id, &imp, "", "").await);
        assert!(
            back.post.is_none(),
            "a cancelled checkout goes back to the impact without draft: {back:?}"
        );
        must(g.withdraw_impact(&ctx, &c.id, &imp, "", "").await);
    })
    .await;
}

// The node index hears of a version when it is written, at landing (ADR 0079): not for the draft, nor for its
// acceptance.
#[tokio::test]
async fn node_event_at_landing() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_algo_world(r).await;
        let sink = Arc::new(RecSink::default());
        w.g.observe(sink.clone());
        let g = &w.g;
        let c = w.change().await;
        let cn = must(
            g.impact_node_create(
                &ctx,
                &c.id,
                NodeCreate {
                    key: "R8".into(),
                    r#type: "Req".into(),
                    properties: props(&[("code", Value::from("REQ-8"))]),
                    rationale: "new".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let id = cn.post.clone().expect("post").id;
        must(edit(g, &c.id, &cn.id, state("approved")).await);
        must(accept(g, &c.id, &cn.id, "bob", "ok").await);
        let events = || {
            sink.0
                .lock()
                .iter()
                .filter(|(_, e)| matches!(e, GraphEvent::Node(ev) if ev.id == id))
                .count()
        };
        assert_eq!(events(), 0, "no node event for a draft");
        must(apply(g, &c.id, "").await);
        assert_eq!(
            events(),
            1,
            "one node event for the version written at landing"
        );
    })
    .await;
}
