//! A root change is rebased onto the head of the branch it was forked from before it
//! lands, and lands as a fast-forward (ADR 0101, 0102).

use std::collections::BTreeMap;

use goap_domain::*;
use goap_graphapi::{NodeCreate, Resolution};
use serde_json::{Map, Value, json};

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, HEADER_BASELINE_ID, NewChange};

/// propChange opens a change from base that sets props on a node of the fixture, accepted.
pub(crate) async fn prop_change(
    g: &Graph,
    base: &BaselineID,
    title: &str,
    node: NodeRef,
    p: Map<String, Value>,
) -> (Change, ChangeImpactID) {
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
                pre: Some(node),
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
    (c, ns[0].id.clone())
}

/// baselinesOf lists the baselines of a branch of the default namespace, in the order they were made.
async fn baselines_of(g: &Graph, branch: &str) -> Vec<Baseline> {
    must(g.baselines(&ctx(), "").await)
        .into_iter()
        .filter(|b| branch_of(&b.branch) == branch)
        .collect()
}

pub(crate) async fn baseline_of_change(g: &Graph, c: &ChangeID, kind: &str) -> Baseline {
    must(g.baselines(&ctx(), "").await)
        .into_iter()
        .find(|b| &b.change_id == c && b.kind == kind)
        .unwrap_or_else(|| panic!("no {kind} baseline of change {c}"))
}

/// rebaseMoves are the baselineId moves recorded in the log of a change.
async fn rebase_moves(g: &Graph, id: &ChangeID) -> Vec<HeaderValue> {
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
    let mut out = Vec::new();
    for e in entries {
        if e.r#type != format!("{LOG_CHANGE}.updated") {
            continue;
        }
        let h: HeaderEdit = serde_json::from_value(e.payload).expect("header edit");
        if let Some(v) = h.fields.get(HEADER_BASELINE_ID) {
            out.push(v.clone());
        }
    }
    out
}

/// impactEventsOf is the impact log of a change.
pub(crate) async fn impact_events_of(g: &Graph, change: &ChangeID) -> Vec<ImpactEvent> {
    let ctx = ctx();
    let mut tx = must(g.begin(&ctx).await);
    let out = must(crate::changelog::impact_events(&ctx, &mut tx, change).await);
    must(tx.commit(&ctx).await);
    out
}

async fn rebased_events(g: &Graph, id: &ChangeID) -> Vec<ImpactEvent> {
    impact_events_of(g, id)
        .await
        .into_iter()
        .filter(|e| e.op == IMPACT_TRANSITIONED && e.patch.get("rebased").is_some())
        .collect()
}

fn num(n: &Node, k: &str) -> Option<f64> {
    n.properties.get(k).and_then(Value::as_f64)
}

// test1 and test2 start from the same baseline and change different nodes: test2 moves onto test1's result, main
// reads as two fast-forwards, the rebase is in test2's log and is a baseline of its branch.
#[tokio::test]
async fn rebase_root_different_nodes() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (t1, _) = prop_change(
            g,
            &f.base.id,
            "test1",
            f.req.r#ref(),
            props(&[("title", json!("T1"))]),
        )
        .await;
        let (t2, _) = prop_change(
            g,
            &f.base.id,
            "test2",
            f.need.r#ref(),
            props(&[("title", json!("T2"))]),
        )
        .await;
        let b1 = must(apply(g, &t1.id, "").await);
        let b2 = must(apply(g, &t2.id, "").await);
        assert!(
            b1.kind == BASELINE_FAST_FORWARD
                && b1.parent_id == f.base.id
                && b2.kind == BASELINE_FAST_FORWARD
                && b2.parent_id == b1.id,
            "main must read T1 then T2: {b1:?} {b2:?}"
        );
        for b in baselines_of(g, MAIN_BRANCH).await {
            assert!(b.merged_from.is_empty(), "a merge baseline on main: {b:?}");
        }
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        assert_eq!(head.id, b2.id, "head");
        let moves = rebase_moves(g, &t2.id).await;
        assert!(
            moves.len() == 1
                && moves[0].from == json!(f.base.id.to_string())
                && moves[0].to == json!(b1.id.to_string()),
            "test2's log must say it moved from B to T1: {moves:?}"
        );
        let got = rebase_moves(g, &t1.id).await;
        assert!(got.is_empty(), "test1 was not behind: {got:?}");
        // ADR 0102: test2 committed on its branch from B, the merge rebased the branch onto T1 (a rebase baseline),
        // then fast-forwarded
        let got = baseline_of_change(g, &t2.id, BASELINE_COMMIT).await;
        assert!(
            got.rebased_from.is_empty() && got.parent_id == f.base.id,
            "test2's commit: {got:?}"
        );
        let got = baseline_of_change(g, &t2.id, BASELINE_REBASE).await;
        assert!(
            got.rebased_from == f.base.id && got.merged_from == b1.id,
            "test2's rebase: {got:?}"
        );
        let got = baseline_of_change(g, &t1.id, BASELINE_COMMIT).await;
        assert!(
            got.rebased_from.is_empty(),
            "test1's commit was never rebased: {got:?}"
        );
        let br = must(g.branch(&ctx, "", &t2.branch).await);
        assert!(
            br.rebased_from == f.base.id && br.fork_baseline == b1.id && br.status == BRANCH_MERGED,
            "test2's branch: {br:?}"
        );
        let ch = must(g.change(&ctx, &t2.id).await);
        assert!(
            ch.baseline_id == b1.id
                && ch.status == CHANGE_APPLIED
                && ch.result_baseline_id == b2.id,
            "test2: {ch:?}"
        );
    })
    .await;
}

// Both change the same node, different properties: test2's commit is merged with the version test1 wrote at its
// merge, and the version that lands is parented to both.
#[tokio::test]
async fn rebase_root_same_node_linear() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (t1, _) = prop_change(
            g,
            &f.base.id,
            "test1",
            f.req.r#ref(),
            props(&[("a", json!(1))]),
        )
        .await;
        let (t2, i2) = prop_change(
            g,
            &f.base.id,
            "test2",
            f.req.r#ref(),
            props(&[("b", json!(2))]),
        )
        .await;
        let b1 = must(apply(g, &t1.id, "").await);
        let v1 = crate::tests::changebranch::head_node(g, &f.req.id).await;

        // before its commit there is nothing to rebase (ADR 0102); committed, test2 is behind main
        let st = must(g.rebase_state(&ctx, &t2.id).await);
        assert!(
            st.onto.is_empty(),
            "an uncommitted change is not behind: {st:?}"
        );
        must(commit_change(g, &t2.id, "").await);
        let st = must(g.rebase_state(&ctx, &t2.id).await);
        assert!(
            st.branch == MAIN_BRANCH && st.onto == b1.id && st.behind == [i2.clone()],
            "test2 is behind main: {st:?}"
        );
        let b2 = must(apply(g, &t2.id, "").await);
        assert!(
            b2.kind == BASELINE_FAST_FORWARD && b2.parent_id == b1.id && b2.merged_from.is_empty(),
            "T2 must fast-forward from T1: {b2:?}"
        );
        // test2's commit, then the merge of it with test1's version: both its parents (ADR 0102)
        let n = crate::tests::changebranch::head_node(g, &f.req.id).await;
        assert!(
            n.change_id == t2.id
                && n.parents.len() == 2
                && n.parents[1] == v1.version
                && num(&n, "a") == Some(1.0)
                && num(&n, "b") == Some(2.0),
            "test2's version = {n:?}, test1's = {v1:?}"
        );
        let ev = rebased_events(g, &t2.id).await;
        assert!(
            ev.len() == 1
                && ev[0].draft.as_ref().and_then(|d| d.merged_with.as_ref()) == Some(&v1.r#ref()),
            "test2's log must hold the merge of its commit with {:?}: {ev:?}",
            v1.r#ref()
        );
        let rb = &ev[0].patch["rebased"];
        assert!(
            rb["branch"] == MAIN_BRANCH && rb["baseline"] == json!(b1.id.to_string()),
            "rebased patch: {:?}",
            ev[0].patch
        );
        assert_eq!(
            rebase_moves(g, &t2.id).await.len(),
            1,
            "one move in the log"
        );
        // the review of the merged draft was kept, the version landed
        let cn = must(seen_impact(g, &t2.id, &i2, "").await);
        assert!(
            cn.review == REVIEW_ACCEPTED && cn.landed.as_ref() == Some(&n.r#ref()),
            "test2's impact: {cn:?}"
        );
    })
    .await;
}

// The same property changed on both sides stops Apply, sends the impact back to proposed, and is settled in the
// change.
#[tokio::test]
async fn rebase_root_conflict() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (t1, _) = prop_change(
            g,
            &f.base.id,
            "test1",
            f.req.r#ref(),
            props(&[("x", json!(1))]),
        )
        .await;
        let (t2, i2) = prop_change(
            g,
            &f.base.id,
            "test2",
            f.req.r#ref(),
            props(&[("x", json!(2))]),
        )
        .await;
        let (t3, i3) = prop_change(
            g,
            &f.base.id,
            "test3",
            f.req.r#ref(),
            props(&[("x", json!(3))]),
        )
        .await;
        must(apply(g, &t1.id, "").await);

        let err = apply(g, &t2.id, "").await.unwrap_err();
        assert!(
            err.is_conflict() && err.to_string().contains("props.x"),
            "apply must stop on the conflict: {err}"
        );
        let cn = must(seen_impact(g, &t2.id, &i2, "").await);
        assert!(
            cn.review == REVIEW_PROPOSED
                && cn.reviews.last().map(|r| r.comment.as_str()) == Some("rebased onto main"),
            "the impact goes back to proposed: {cn:?}"
        );
        let ch = must(g.change(&ctx, &t2.id).await);
        assert_eq!(ch.status, CHANGE_ACTIVE, "test2");
        let st = must(g.rebase_state(&ctx, &t2.id).await);
        assert!(
            st.conflicts.len() == 1
                && st.conflicts[0].conflicts == ["props.x"]
                && st.behind.is_empty(),
            "state = {st:?}"
        );
        assert_kind(
            g.impact_node_review_on(&ctx, &t2.id, "", "", &i2, REVIEW_ACCEPTED, "u", "ok")
                .await,
            ErrorKind::Conflict,
            "a conflict is settled before the review",
        );
        // keeping its values settles it
        must(g.impact_node_resolve(&ctx, &t2.id, &i2, "").await);
        must(accept(g, &t2.id, &i2, "u", "ok").await);
        let b2 = must(apply(g, &t2.id, "").await);
        let n = crate::tests::changebranch::head_node(g, &f.req.id).await;
        assert_eq!(
            num(&n, "x"),
            Some(2.0),
            "test2 keeps its value: {:?}",
            n.properties
        );
        // editing the field settles it as well; the second apply rebases onto test2
        assert_kind(apply(g, &t3.id, "").await, ErrorKind::Conflict, "test3");
        must(
            edit(
                g,
                &t3.id,
                &i3,
                Edit {
                    properties: props(&[("x", json!(4))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &t3.id, &i3, "u", "ok").await);
        let b3 = must(apply(g, &t3.id, "").await);
        assert!(
            b3.parent_id == b2.id && b3.kind == BASELINE_FAST_FORWARD,
            "test3 lands on test2: {b3:?}"
        );
    })
    .await;
}

// A node both changes create collides on its key: the commit of the second refuses it, settled by withdrawing the
// creation.
#[tokio::test]
async fn rebase_root_creation_collision() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let create = async |title: &str| {
            let c = must(
                g.create_change(
                    &ctx,
                    NewChange {
                        project_id: "PROJ-ROOT".into(),
                        title: title.into(),
                        baseline_id: f.base.id.clone(),
                        own_branch: true,
                        ..Default::default()
                    },
                )
                .await,
            );
            let cn = must(
                g.impact_node_create(
                    &ctx,
                    &c.id,
                    NodeCreate {
                        key: "REQ-9".into(),
                        r#type: "Requirement".into(),
                        rationale: title.into(),
                        ..Default::default()
                    },
                )
                .await,
            );
            must(
                edit(
                    g,
                    &c.id,
                    &cn.id,
                    Edit {
                        properties: props(&[("title", json!(title))]),
                        ..Default::default()
                    },
                )
                .await,
            );
            must(accept(g, &c.id, &cn.id, "u", "ok").await);
            (c, cn.id)
        };
        let (t1, _) = create("test1").await;
        let (t2, i2) = create("test2").await;
        must(apply(g, &t1.id, "").await);
        // the commit of test2 writes its REQ-9: the key is test1's now
        let err = apply(g, &t2.id, "").await.unwrap_err();
        assert!(
            err.is_conflict()
                && err
                    .to_string()
                    .contains("REQ-9 (Requirement): the key is taken"),
            "the collision stops the apply: {err}"
        );
        must(g.withdraw_impact(&ctx, &t2.id, &i2, "", "").await);
        assert_kind(
            apply(g, &t2.id, "").await,
            ErrorKind::NothingToCommit,
            "test2 is left with nothing to commit (ADR 0102)",
        );
        let n = must(g.node_by_key(&ctx, "", "REQ-9").await);
        assert!(
            n.properties["title"] == "test1" && n.change_id == t1.id,
            "REQ-9 stays test1's: {n:?}"
        );
    })
    .await;
}

// ADR 0102: a committed change stays open; when the target moved, its merge rebases its branch first (the nodes
// changed on both sides merged into drafts, committed when clean) and lands as a fast-forward.
#[tokio::test]
async fn rebase_root_committed_rebased_at_merge() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (t1, _) = prop_change(
            g,
            &f.base.id,
            "test1",
            f.req.r#ref(),
            props(&[("a", json!(1))]),
        )
        .await;
        let (t2, _) = prop_change(
            g,
            &f.base.id,
            "test2",
            f.req.r#ref(),
            props(&[("b", json!(2))]),
        )
        .await;
        let c2 = must(commit_change(g, &t2.id, "").await);
        let ch = must(g.change(&ctx, &t2.id).await);
        assert!(
            ch.status == CHANGE_ACTIVE && ch.result_baseline_id == c2.id,
            "a committed change stays open: {ch:?}"
        );
        let b1 = must(apply(g, &t1.id, "").await);

        let st = must(g.rebase_state(&ctx, &t2.id).await);
        assert!(
            st.onto == b1.id && st.behind.len() == 1,
            "test2 is behind test1 on REQ-1: {st:?}"
        );
        let ch = must(integrate_change(g, &t2.id).await);
        assert_eq!(ch.status, CHANGE_APPLIED, "merge");
        let res = must(g.baseline(&ctx, &ch.result_baseline_id).await);
        assert!(
            res.kind == BASELINE_FAST_FORWARD && res.parent_id == b1.id,
            "the merge of a rebased branch is a fast-forward: {res:?}"
        );
        let n = must(
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
            n.properties.contains_key("a")
                && n.properties.contains_key("b")
                && n.parents.len() == 2,
            "REQ-1 holds both changes, merged from both versions: {n:?}"
        );
        let rb = baseline_of_change(g, &t2.id, BASELINE_REBASE).await;
        assert!(
            rb.rebased_from == f.base.id && rb.parent_id == c2.id && rb.merged_from == b1.id,
            "the rebase of the branch: {rb:?}"
        );
    })
    .await;
}

// The target moves between the rebase and the integration: the fast-forward is impossible, the three-way merge runs
// and waits for the conflict to be resolved.
#[tokio::test]
async fn rebase_root_race_falls_back_to_merge() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (t1, _) = prop_change(
            g,
            &f.base.id,
            "test1",
            f.req.r#ref(),
            props(&[("x", json!(1))]),
        )
        .await;
        let (t2, _) = prop_change(
            g,
            &f.base.id,
            "test2",
            f.req.r#ref(),
            props(&[("x", json!(2))]),
        )
        .await;
        let (t0, _) = prop_change(
            g,
            &f.base.id,
            "test0",
            f.need.r#ref(),
            props(&[("title", json!("N"))]),
        )
        .await;
        must(apply(g, &t0.id, "").await);
        must(commit_change(g, &t2.id, "").await);
        must(g.rebase_change(&ctx, &t2.id).await); // test2 brought up to date with test0...
        let b1 = must(apply(g, &t1.id, "").await); // ...then the target moves before test2 lands
        {
            let mut tx = must(g.begin(&ctx).await);
            let c = must(crate::repo::Tx::change(&mut tx, &ctx, &t2.id).await);
            let c = must(g.integrate_tx(&ctx, &mut tx, c, &BTreeMap::new()).await);
            assert_eq!(
                c.status, CHANGE_COMMITTED,
                "a conflicting integration waits"
            );
            must(tx.commit(&ctx).await);
        }
        let mut res = BTreeMap::new();
        res.insert(
            f.req.id.clone(),
            Resolution {
                props: props(&[("title", json!("Use PSP v1")), ("x", json!(3))]),
                ..Default::default()
            },
        );
        let ch = must(g.integrate_change(&ctx, &t2.id, &res).await);
        assert_eq!(ch.status, CHANGE_APPLIED, "resolution");
        let res = must(g.baseline(&ctx, &ch.result_baseline_id).await);
        assert!(
            res.kind == BASELINE_MERGE && res.parent_id == b1.id && !res.merged_from.is_empty(),
            "the race lands by a merge: {res:?}"
        );
        let got = baseline_of_change(g, &t2.id, BASELINE_REBASE).await;
        assert_eq!(
            got.rebased_from, f.base.id,
            "test2 was rebased onto test0 before"
        );
    })
    .await;
}
