//! A change lands on every branch it leaves its state on (ADR 0056, 0102, 0103).

use goap_domain::*;
use goap_graphapi::{Commit, NodeEdit};
use serde_json::json;

use crate::tests::helpers::*;
use crate::tests::rebase_root::impact_events_of;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

async fn landings(g: &Graph, change: &ChangeID) -> Vec<ImpactEvent> {
    impact_events_of(g, change)
        .await
        .into_iter()
        .filter(|e| e.op == IMPACT_LANDED)
        .collect()
}

fn created(key: &str, rationale: &str) -> ChangeImpact {
    ChangeImpact {
        intent: INTENT_CREATED,
        key: key.into(),
        r#type: "Design".into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

// A change lands on its own branch when committed, and again on the branch it was forked from when merged into it:
// the last landing is the version on the target. A sub-change lands on its branch, then on its parent's (ADR 0103).
// Go: TestLandedOnEveryBranchTheChangeLeavesItsState
#[tokio::test]
async fn landed_on_every_branch() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;

        // Commit opens a branch of its own: applied on it, then merged into main
        let res = must(
            g.commit(
                &ctx,
                Commit {
                    project_id: "PROJ-ROOT".into(),
                    title: "own".into(),
                    baseline: f.base.id.clone(),
                    by: "test".into(),
                    edits: vec![NodeEdit {
                        key: "L-1".into(),
                        r#type: "Design".into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        let ev = landings(g, &res.change).await;
        assert_eq!(
            ev.len(),
            2,
            "a change with its own branch lands twice: {ev:?}"
        );
        let on_branch = must(g.baseline(&ctx, &ev[0].baseline).await);
        let on_main = must(g.baseline(&ctx, &ev[1].baseline).await);
        assert!(
            on_branch.branch != MAIN_BRANCH
                && on_main.branch == MAIN_BRANCH
                && on_main.id == res.baseline.id,
            "landed on {:?}, then on {:?} (result {})",
            on_branch.branch,
            on_main.branch,
            res.baseline.id
        );
        assert!(
            on_branch.contains(ev[0].landed.as_ref().unwrap())
                && on_main.contains(ev[1].landed.as_ref().unwrap()),
            "a landing names a version its baseline does not hold: {ev:?}"
        );
        let c = must(g.change(&ctx, &res.change).await);
        assert!(
            c.nodes.len() == 1 && c.nodes[0].landed == ev[1].landed,
            "the change impact holds the last landing: {:?}",
            c.nodes
        );

        // a sub-change commits on its own branch, forked from its parent's, and lands on it at its merge (ADR 0103)
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let parent = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "parent".into(),
                    baseline_id: head.id.clone(),
                    own_branch: true,
                    ..Default::default()
                },
            )
            .await,
        );
        let sub = must(
            g.create_change(
                &ctx,
                NewChange {
                    title: "sub".into(),
                    parent_id: parent.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let added = must(propose_or_create(g, &sub.id, vec![created("L-2", "sub")]).await);
        must(
            edit(
                g,
                &sub.id,
                &added[0].id,
                Edit {
                    properties: props(&[("title", json!("t"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &sub.id, &added[0].id, "test", "ok").await);
        let b = must(apply(g, &sub.id, "").await);
        let parent = must(g.change(&ctx, &parent.id).await);
        let ev = landings(g, &sub.id).await;
        assert!(
            ev.len() == 2
                && ev[1].branch == branch_of(&parent.branch)
                && b.branch == branch_of(&parent.branch),
            "a sub-change lands on its branch, then on its parent's (baseline {b:?}): {ev:?}"
        );
        let pl = landings(g, &parent.id).await;
        assert!(
            pl.len() == 1 && pl[0].landed == ev[1].landed,
            "the parent holds what its sub-change landed: {pl:?}"
        );
        must(apply(g, &parent.id, "").await);
        let ev = landings(g, &parent.id).await;
        assert!(
            ev.len() == 2 && ev[0].impact == ev[1].impact && ev[1].branch == MAIN_BRANCH,
            "the parent holds the sub-change's landing on its branch, then lands it on main: {ev:?}"
        );
        let n = must(g.node_by_key(&ctx, "", "L-2").await);
        assert!(
            n.properties["title"] == "t" && n.change_id == parent.id,
            "L-2 on main = {n:?}"
        );
    })
    .await;
}

// Committing validates a change and records its state on its own branch; integrating lands it on its parent branch
// (ADR 0056). Apply is both.
#[tokio::test]
async fn commit_then_integrate() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let head = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let c = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "split".into(),
                    baseline_id: head.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let added = must(propose_or_create(g, &c.id, vec![created("S-1", "split")]).await);
        must(
            edit(
                g,
                &c.id,
                &added[0].id,
                Edit {
                    properties: props(&[("title", json!("t"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &c.id, &added[0].id, "test", "ok").await);
        // committed: its state is on its own branch, main did not move
        let commit = must(commit_change(g, &c.id, "").await);
        let got = must(g.change(&ctx, &c.id).await);
        assert!(
            got.status == CHANGE_ACTIVE
                && got.result_baseline_id == commit.id
                && commit.kind == BASELINE_COMMIT
                && commit.branch == got.branch
                && commit.branch != MAIN_BRANCH
                && commit.change_id == c.id,
            "committed change {got:?}, commit baseline {commit:?}"
        );
        let now = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        assert_eq!(now.id, head.id, "committing moved main");
        let err = commit_change(g, &c.id, "").await.unwrap_err();
        assert!(err.is_invalid(), "committing again with nothing new: {err}");
        // a committed change stays open (ADR 0102): it takes impacts again
        let late = must(propose_or_create(g, &c.id, vec![created("S-2", "late")]).await);
        must(g.withdraw_impact(&ctx, &c.id, &late[0].id, "", "").await);
        // integrated: applied, a fast-forward onto main
        let done = must(integrate_change(g, &c.id).await);
        assert!(
            done.status == CHANGE_APPLIED && done.result_baseline_id != commit.id,
            "integrated change {done:?}"
        );
        let on_main = must(g.baseline(&ctx, &done.result_baseline_id).await);
        assert!(
            on_main.branch == MAIN_BRANCH
                && on_main.kind == BASELINE_FAST_FORWARD
                && on_main.change_id == c.id
                && on_main.nodes == commit.nodes,
            "integration baseline {on_main:?}, commit baseline nodes {:?}",
            commit.nodes
        );
        assert_kind(
            integrate_change(g, &c.id).await,
            ErrorKind::Conflict,
            "integrating an applied change",
        );
        // a change that wrote nothing has nothing to commit (ADR 0102)
        let empty = must(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "nothing".into(),
                    baseline_id: on_main.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let err = commit_change(g, &empty.id, "").await.unwrap_err();
        assert!(err.is_invalid(), "an empty change is not committed: {err}");
    })
    .await;
}
