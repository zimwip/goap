//! Port of `review_test.go`: the review object (ADR 0080, 0105): batches, their atomicity, the use case of
//! `goap-review` over the graph, the flows, and the origins settled first.

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{OpenOptionRequest, Restructured};
use goap_review as review;
use serde_json::json;

use crate::changelog::impact_events;
use crate::tests::changeimpact::{modified, plain_change};
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::tests::restructure::new_docs;
use crate::testsupport::*;
use crate::{ErrorKind, Graph};

/// The graph as the port of the review use case (Go's Graph implements review.Port itself).
struct Port(Arc<Graph>);

fn port_err(e: crate::Error) -> review::Error {
    review::Error::port(e)
}

#[async_trait]
impl review::Port for Port {
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, review::Error> {
        self.0.change(ctx, id).await.map_err(port_err)
    }
    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, review::Error> {
        self.0.add_items(ctx, id, items).await.map_err(port_err)
    }
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, review::Error> {
        self.0
            .impact_node_review_batch(ctx, id, b)
            .await
            .map_err(port_err)
    }
}

/// The kind of the graph's error a refusal of the port carries.
fn port_kind(e: &review::Error) -> Option<ErrorKind> {
    match e {
        review::Error::Port(b) => b.downcast_ref::<crate::Error>().map(|e| e.kind()),
        _ => None,
    }
}

/// reviewWorld is a change proposing the three nodes of the fixture: three impacts awaiting review.
async fn review_world(r: &TestRepo) -> (Fixture, Change, Vec<ChangeImpact>, review::Service) {
    let ctx = ctx();
    let f = new_fixture(r).await;
    let c = must(
        f.g.create_change(&ctx, plain_change(&f.base, "review"))
            .await,
    );
    let nodes = [&f.need, &f.req, &f.test]
        .iter()
        .map(|n| ChangeImpact {
            produced_by: "bob".into(),
            ..modified(n, "r")
        })
        .collect();
    let mut got = must(f.g.propose_impact(&ctx, &c.id, nodes).await);
    // each impact modifies its node: only a modification is accepted (ADR 0102)
    for cn in got.iter_mut() {
        *cn = must(
            edit(
                &f.g,
                &c.id,
                &cn.id,
                Edit {
                    properties: title(&format!("reviewed {}", cn.key)),
                    ..Default::default()
                },
            )
            .await,
        );
    }
    let svc = review::Service::new(Arc::new(Port(Arc::new(r.graph()))));
    (f, c, got, svc)
}

async fn reviewed_events(g: &Graph, id: &ChangeID) -> Vec<ImpactEvent> {
    let ctx = ctx();
    let mut tx = must(g.begin(&ctx).await);
    must(impact_events(&ctx, &mut tx, id).await)
        .into_iter()
        .filter(|e| e.op == IMPACT_REVIEWED)
        .collect()
}

async fn must_change(g: &Graph, id: &ChangeID) -> Change {
    must(g.change(&ctx(), id).await)
}

fn review_item(key: &str, entries: serde_json::Value) -> ChangeItem {
    ChangeItem {
        r#type: review::KIND_REVIEW,
        data: json!({"key": key, "status": review::SUBMITTED, "submittedAt": "2026-01-01T00:00:00Z", "entries": entries})
            .as_object()
            .cloned()
            .unwrap(),
        ..Default::default()
    }
}

fn verdict(impact: &ChangeImpactID, status: NodeReview, comment: &str) -> ImpactVerdict {
    ImpactVerdict {
        impact: impact.clone(),
        status,
        comment: comment.into(),
    }
}

/// causedVerdict is a reviewed entry of the log and the entry it names as its cause.
#[derive(Debug)]
struct CausedVerdict {
    seq: i64,
    cause: LogEntry,
}

/// causesOf lists the decided reviewed entries of the log of a change with their cause, which must be a fact.review
/// entry.
async fn causes_of(g: &Graph, id: &ChangeID) -> Vec<CausedVerdict> {
    let (entries, _) = must(
        g.change_log(
            &ctx(),
            &LogFilter {
                change: id.clone(),
                ..Default::default()
            },
        )
        .await,
    );
    let mut by_id = std::collections::HashMap::new();
    let mut out = Vec::new();
    for e in entries {
        by_id.insert(e.id.clone(), e.clone());
        if e.r#type != format!("{LOG_IMPACT}.{IMPACT_REVIEWED}") || e.causation.is_empty() {
            continue;
        }
        let cause = by_id.get(&e.causation).cloned();
        let Some(cause) = cause.filter(|c| c.r#type == format!("{LOG_FACT}.{TYPE_REVIEW}")) else {
            panic!(
                "entry {} names cause {:?}, not an earlier review",
                e.seq, e.causation
            );
        };
        out.push(CausedVerdict { seq: e.seq, cause });
    }
    out
}

/// rs0 is the item holding the last version of a review.
async fn rs0(g: &Graph, id: &ChangeID, key: &str) -> ItemID {
    let c = must_change(g, id).await;
    review::find(&c.items, key)
        .unwrap_or_else(|| panic!("no review {key}"))
        .item
}

// The batch reviews several impacts in one transaction, each with its own comment and outcome, every review carrying
// the id of the batch, and writes its item with them (ADR 0080).
#[tokio::test]
async fn review_batch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let (f, c, imps, _) = review_world(r).await;
        let g = &f.g;
        let item = review_item(
            "REV-1",
            json!([{"impact": imps[0].id, "outcome": "accept"}, {"impact": imps[1].id, "outcome": "reject"}]),
        );
        let out = must(
            g.impact_node_review_batch(
                &ctx,
                &c.id,
                ReviewBatch {
                    id: "REV-1".into(),
                    by: "alice".into(),
                    item: Some(item),
                    verdicts: vec![
                        verdict(&imps[0].id, REVIEW_ACCEPTED, "fine"),
                        verdict(&imps[1].id, REVIEW_REJECTED, "no"),
                    ],
                    ..Default::default()
                },
            )
            .await,
        );
        assert_eq!(out.len(), 2, "batch: {out:?}");
        let ch = must_change(g, &c.id).await;
        assert!(
            ch.nodes[0].review == REVIEW_ACCEPTED
                && ch.nodes[1].review == REVIEW_REJECTED
                && ch.nodes[2].review == REVIEW_PROPOSED,
            "outcomes: {:?} {:?} {:?}",
            ch.nodes[0].review,
            ch.nodes[1].review,
            ch.nodes[2].review
        );
        let rv = &ch.nodes[0].reviews[0];
        assert!(
            rv.review_id == "REV-1" && rv.by == "alice" && rv.comment == "fine",
            "review: {rv:?}"
        );
        let evs = reviewed_events(g, &c.id).await;
        assert!(
            evs.len() == 2
                && evs[0].review.as_ref().unwrap().review_id == "REV-1"
                && evs[1].review.as_ref().unwrap().review_id == "REV-1",
            "events: {evs:?}"
        );
        let rs = review::reviews(&ch.items);
        assert!(rs.len() == 1 && rs[0].status == review::SUBMITTED, "item: {rs:?}");
        // the record of the review comes first, and is the cause of every verdict it decides (ADR 0105)
        let causes = causes_of(g, &c.id).await;
        let rev1 = rs0(g, &c.id, "REV-1").await;
        assert!(
            causes.len() == 2
                && causes[0].cause.id == rev1.as_str()
                && causes[1].cause.id == causes[0].cause.id
                && causes[0].cause.seq < causes[0].seq,
            "causation: {causes:?}"
        );
        // a verdict on its own is a review of one entry, submitted at once
        must(accept(g, &c.id, &imps[2].id, "carol", "ok").await);
        let evs = reviewed_events(g, &c.id).await;
        let ch = must_change(g, &c.id).await;
        let lone = review::find(&ch.items, &evs[2].review.as_ref().unwrap().review_id);
        let want = review::Entry {
            impact: imps[2].id.to_string(),
            comment: "ok".into(),
            outcome: review::ACCEPT.into(),
        };
        assert!(
            lone.as_ref().is_some_and(|l| l.status == review::SUBMITTED
                && l.by == "carol"
                && l.entries == vec![want.clone()]),
            "a lone verdict is a submitted review of one entry: {lone:?}"
        );
        let causes = causes_of(g, &c.id).await;
        assert!(
            causes.len() == 3 && causes[2].cause.id == lone.unwrap().item.as_str(),
            "the lone verdict is caused by its review: {causes:?}"
        );
    })
    .await;
}

// One refused verdict refuses the whole batch: nothing is written, the error names the entry (ADR 0080).
#[tokio::test]
async fn review_batch_is_atomic() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let (mut f, c, imps, _) = review_world(r).await;
        must(accept(&f.g, &c.id, &imps[1].id, "alice", "already").await);
        let log_len = async |g: &Graph| {
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
        let before = log_len(&f.g).await;
        let item = review_item(
            "REV-2",
            json!([{"impact": imps[0].id, "outcome": "accept"}, {"impact": imps[1].id, "outcome": "accept"}]),
        );
        assert_err(
            f.g.impact_node_review_batch(
                &ctx,
                &c.id,
                ReviewBatch {
                    id: "REV-2".into(),
                    by: "bob".into(),
                    item: Some(item),
                    verdicts: vec![
                        verdict(&imps[0].id, REVIEW_ACCEPTED, "ok"),
                        verdict(&imps[1].id, REVIEW_ACCEPTED, "twice"),
                    ],
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            &["verdict 2 (REQ-1)"],
            "the failing entry is named",
        );
        let ch = must_change(&f.g, &c.id).await;
        // the review of the earlier lone verdict
        assert!(
            ch.nodes[0].review == REVIEW_PROPOSED
                && ch.nodes[0].reviews.is_empty()
                && log_len(&f.g).await == before
                && review::reviews(&ch.items).len() == 1,
            "a refused batch writes nothing: {:?}",
            ch.nodes[0]
        );
        // the review policy refuses per impact too, whatever the position
        struct Refuse;
        impl ReviewPolicy for Refuse {
            fn review(&self, q: &ReviewRequest) -> std::result::Result<(), goap_domain::Error> {
                if q.reviewer == "mallory" {
                    return Err(goap_domain::Error::Invalid("no".into()));
                }
                Ok(())
            }
        }
        f.g.review_policy = Some(Arc::new(Refuse));
        let g = &f.g;
        let batch = |by: &str, vs: Vec<ImpactVerdict>| ReviewBatch {
            by: by.into(),
            verdicts: vs,
            ..Default::default()
        };
        assert_kind(
            g.impact_node_review_batch(
                &ctx,
                &c.id,
                batch("mallory", vec![verdict(&imps[0].id, REVIEW_ACCEPTED, "x")]),
            )
            .await,
            ErrorKind::Invalid,
            "policy",
        );
        let id0 = &imps[0].id;
        let bad = vec![
            batch("a", vec![]), // no verdict
            batch("a", vec![verdict(id0, REVIEW_PROPOSED, "x")]),
            batch("a", vec![verdict(id0, REVIEW_ACCEPTED, "")]),
            batch(
                "a",
                vec![verdict(id0, REVIEW_ACCEPTED, "x"), verdict(id0, REVIEW_REJECTED, "y")],
            ),
            ReviewBatch {
                item: Some(ChangeItem {
                    r#type: TYPE_TRANSITION,
                    data: json!({"to": "x"}).as_object().cloned().unwrap(),
                    ..Default::default()
                }),
                ..batch("a", vec![verdict(id0, REVIEW_ACCEPTED, "x")])
            },
        ];
        for b in bad {
            let what = format!("{b:?}");
            assert_kind(
                g.impact_node_review_batch(&ctx, &c.id, b).await,
                ErrorKind::Invalid,
                &what,
            );
        }
    })
    .await;
}

fn s(v: &str) -> Option<String> {
    Some(v.to_string())
}

// The operations of the use case: open, add and remove impacts, edit entries, submit, final once submitted.
#[tokio::test]
async fn review_object() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let (f, c, imps, svc) = review_world(r).await;
        let g = &f.g;
        let alice = review::Actor {
            subject: "alice".into(),
            admin: false,
        };
        let admin = review::Actor {
            subject: "root".into(),
            admin: true,
        };
        let mallory = review::Actor {
            subject: "mallory".into(),
            admin: false,
        };
        let id = |i: usize| imps[i].id.to_string();
        let entry =
            |i: usize, comment: Option<String>, outcome: Option<String>| review::EntryEdit {
                impact: id(i),
                comment,
                outcome,
            };

        let rv = svc
            .open(&ctx, &c.id, "", " global ", &alice)
            .await
            .expect("open");
        assert!(
            rv.status == review::OPEN
                && rv.by == "alice"
                && rv.comment == "global"
                && rv.entries.is_empty(),
            "open: {rv:?}"
        );
        let rv = svc
            .update(
                &ctx,
                &c.id,
                &rv.key,
                review::Edit {
                    add: vec![id(0), id(1)],
                    entries: vec![entry(0, s("a"), s("accept"))],
                    ..Default::default()
                },
                &alice,
            )
            .await
            .expect("add");
        assert!(
            rv.entries.len() == 2
                && rv.entries[0].outcome == review::ACCEPT
                && rv.entries[0].comment == "a"
                && rv.entries[1].outcome.is_empty(),
            "add: {rv:?}"
        );
        // an impact is in one open review per flow
        let other = svc.open(&ctx, &c.id, "", "", &alice).await.expect("open");
        let add = |i: usize| review::Edit {
            add: vec![id(i)],
            ..Default::default()
        };
        let err = svc
            .update(&ctx, &c.id, &other.key, add(1), &alice)
            .await
            .unwrap_err();
        assert!(
            err.is_conflict() && err.to_string().contains(&rv.key),
            "an impact in two open reviews: {err}"
        );
        let err = svc
            .update(
                &ctx,
                &c.id,
                &other.key,
                review::Edit {
                    add: vec!["nope".into()],
                    ..Default::default()
                },
                &alice,
            )
            .await
            .unwrap_err();
        assert!(err.is_not_found(), "unknown impact: {err}");
        // what awaits review leaves the list once taken, and comes back when removed
        let ch = must_change(g, &c.id).await;
        let aw = review::awaiting(&ch, "", &review::reviews(&ch.items), "");
        assert!(aw.len() == 1 && aw[0].id == imps[2].id, "awaiting: {aw:?}");
        let rv = svc
            .update(
                &ctx,
                &c.id,
                &rv.key,
                review::Edit {
                    remove: vec![id(1)],
                    ..Default::default()
                },
                &alice,
            )
            .await
            .expect("remove");
        assert_eq!(rv.entries.len(), 1, "remove: {rv:?}");
        svc.update(&ctx, &c.id, &other.key, add(1), &alice)
            .await
            .expect("the impact is free again");
        // authorization: the author or an administrator
        let err = svc
            .update(
                &ctx,
                &c.id,
                &rv.key,
                review::Edit {
                    comment: s("hijack"),
                    ..Default::default()
                },
                &mallory,
            )
            .await
            .unwrap_err();
        assert!(err.is_forbidden(), "someone else: {err}");
        let err = svc
            .submit(&ctx, &c.id, &rv.key, &mallory)
            .await
            .unwrap_err();
        assert!(err.is_forbidden(), "someone else submits: {err}");
        svc.update(
            &ctx,
            &c.id,
            &rv.key,
            review::Edit {
                add: vec![id(2)],
                entries: vec![entry(2, None, s("reject"))],
                ..Default::default()
            },
            &admin,
        )
        .await
        .expect("an administrator");
        // every entry decided, a comment on each (its own or the global one)
        let err = svc
            .submit(&ctx, &c.id, "REV-none", &alice)
            .await
            .unwrap_err();
        assert!(err.is_not_found(), "unknown review: {err}");
        let rv = review::find(&must_change(g, &c.id).await.items, &rv.key).unwrap();
        assert!(rv.ready(), "ready: {rv:?}");
        let rv = svc
            .update(
                &ctx,
                &c.id,
                &rv.key,
                review::Edit {
                    entries: vec![entry(2, None, s(""))],
                    ..Default::default()
                },
                &alice,
            )
            .await
            .expect("update");
        assert!(!rv.ready(), "an undecided entry: {rv:?}");
        let err = svc.submit(&ctx, &c.id, &rv.key, &alice).await.unwrap_err();
        assert!(
            err.is_invalid(),
            "submit needs an outcome everywhere: {err}"
        );
        svc.update(
            &ctx,
            &c.id,
            &rv.key,
            review::Edit {
                entries: vec![entry(2, s("not yet"), s("reject"))],
                ..Default::default()
            },
            &alice,
        )
        .await
        .expect("update");
        // the review's own failure leaves it open: TST-1 is accepted by someone else meanwhile
        must(accept(g, &c.id, &imps[2].id, "carol", "meanwhile").await);
        let err = svc.submit(&ctx, &c.id, &rv.key, &alice).await.unwrap_err();
        assert!(
            port_kind(&err) == Some(ErrorKind::Conflict) && err.to_string().contains("verdict 2"),
            "an impact no longer proposed: {err}"
        );
        let got = review::find(&must_change(g, &c.id).await.items, &rv.key).unwrap();
        assert!(
            got.open() && reviewed_events(g, &c.id).await.len() == 1,
            "the review stays open, nothing was reviewed: {got:?}"
        );
        svc.update(
            &ctx,
            &c.id,
            &rv.key,
            review::Edit {
                remove: vec![id(2)],
                ..Default::default()
            },
            &alice,
        )
        .await
        .expect("remove");
        let rv = svc
            .submit(&ctx, &c.id, &rv.key, &alice)
            .await
            .expect("submit");
        assert!(
            rv.status == review::SUBMITTED && rv.submitted_at.is_some() && !rv.item.is_empty(),
            "submit: {rv:?}"
        );
        let ch = must_change(g, &c.id).await;
        assert!(
            ch.nodes[0].review == REVIEW_ACCEPTED
                && ch.nodes[0].reviews[0].review_id == rv.key
                && ch.nodes[0].reviews[0].by == "alice",
            "the impact is reviewed in the review: {:?}",
            ch.nodes[0]
        );
        // the comment of the review keeps both
        assert_eq!(
            ch.nodes[0].reviews[0].comment, "a\n\nReview: global",
            "comment"
        );
        // final
        let err = svc
            .update(
                &ctx,
                &c.id,
                &rv.key,
                review::Edit {
                    comment: s("late"),
                    ..Default::default()
                },
                &alice,
            )
            .await
            .unwrap_err();
        assert!(err.is_conflict(), "a submitted review is final: {err}");
        let err = svc.discard(&ctx, &c.id, &rv.key, &alice).await.unwrap_err();
        assert!(
            err.is_conflict(),
            "a submitted review is not discarded: {err}"
        );
        let err = svc.submit(&ctx, &c.id, &rv.key, &alice).await.unwrap_err();
        assert!(err.is_conflict(), "submitted twice: {err}");
        let d = svc
            .discard(&ctx, &c.id, &other.key, &alice)
            .await
            .expect("discard");
        assert_eq!(d.status, review::DISCARDED, "discard");
        let err = svc
            .update(
                &ctx,
                &c.id,
                &other.key,
                review::Edit {
                    comment: s("x"),
                    ..Default::default()
                },
                &alice,
            )
            .await
            .unwrap_err();
        assert!(err.is_conflict(), "a discarded review is final: {err}");
        // a discarded review frees its impacts
        let ch = must_change(g, &c.id).await;
        let aw = review::awaiting(&ch, "", &review::reviews(&ch.items), "");
        assert!(
            aw.len() == 1 && aw[0].id == imps[1].id,
            "the impact of the discarded review awaits again: {aw:?}"
        );
    })
    .await;
}

// A review takes the impacts of its flow: a review opened while an option is active is on the option, and the
// review of the main flow named explicitly is not taken by it.
#[tokio::test]
async fn review_on_the_main_flow_while_an_option_is_active() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let (f, c, imps, svc) = review_world(r).await;
        let who = review::Actor {
            subject: "alice".into(),
            admin: false,
        };
        let rv = svc
            .open(&ctx, &c.id, MAIN_FLOW, "", &who)
            .await
            .expect("open on main");
        assert!(rv.flow.is_empty(), "open on main: {rv:?}");
        must(
            f.g.open_option(
                &ctx,
                &c.id,
                OpenOptionRequest {
                    name: "opt".into(),
                    hypothesis: "h".into(),
                    activate: true,
                    by: "alice".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        svc.update(
            &ctx,
            &c.id,
            &rv.key,
            review::Edit {
                add: vec![imps[0].id.to_string()],
                entries: vec![review::EntryEdit {
                    impact: imps[0].id.to_string(),
                    outcome: s("accept"),
                    comment: s("c"),
                }],
                ..Default::default()
            },
            &who,
        )
        .await
        .expect("the review stays on its flow");
        let got = svc
            .submit(&ctx, &c.id, &rv.key, &who)
            .await
            .expect("submit on main while an option is active");
        assert!(
            got.flow.is_empty() && got.status == review::SUBMITTED,
            "submit: {got:?}"
        );
        assert_eq!(
            must_change(&f.g, &c.id).await.nodes[0].review,
            REVIEW_ACCEPTED,
            "main flow review"
        );
        let on = svc.open(&ctx, &c.id, "", "", &who).await.expect("open");
        assert!(
            !on.flow.is_empty(),
            "a review with no flow is on the active option: {on:?}"
        );
    })
    .await;
}

// A batch settles the origins of a successor first, whatever the order of its verdicts: the successor is listed
// before the impact deciding the sources it derives from and the batch still stands (ADR 0077 gate, ADR 0080, 0102).
#[tokio::test]
async fn review_batch_settles_origins_first() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let c = d.change().await;
        let res: Restructured = must(d.merge(&c, "C").await);
        let mut vs = vec![verdict(&res.successors[0].id, REVIEW_ACCEPTED, "c")];
        for p in &res.parents {
            // the merge is decided by the impact of the parent (ADR 0102)
            vs.push(verdict(&p.id, REVIEW_ACCEPTED, "ok"));
        }
        must(
            g.impact_node_review_batch(
                &ctx,
                &c.id,
                ReviewBatch {
                    id: "R".into(),
                    by: "alice".into(),
                    verdicts: vs,
                    ..Default::default()
                },
            )
            .await,
        );
        // without the parent in the batch, the gate still refuses it and nothing is written
        let c2 = d.change().await;
        let res2 = must(d.merge(&c2, "C2").await);
        assert_err(
            g.impact_node_review_batch(
                &ctx,
                &c2.id,
                ReviewBatch {
                    by: "alice".into(),
                    verdicts: vec![verdict(&res2.successors[0].id, REVIEW_ACCEPTED, "c")],
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            &["derives from"],
            "the origins gate",
        );
        let got = must_change(g, &c2.id).await;
        assert_eq!(
            got.nodes[0].review, REVIEW_PROPOSED,
            "nothing written: {:?}",
            got.nodes
        );
    })
    .await;
}
