//! Go: attach_test.go.

use goap_engine::{AttachRequest, STATUS_COMPLETED, STATUS_RUNNING, StartRequest};
use goap_graph::NewChange;
use goap_graphapi::Port;
use goap_graphtest as graphtest;
use goap_journal as journal;
use serde_json::Value;

use crate::common::*;

const PSP: &str = "The PSP changes its API, what does this break?";

async fn attaches(
    g: &dyn Port,
    change: &goap_domain::ChangeID,
    process: &str,
) -> Vec<journal::Record> {
    journal::read(
        &ctx(),
        g,
        &journal::Filter {
            change_id: change.clone(),
            process_ids: vec![process.to_string()],
        },
    )
    .await
    .unwrap()
}

fn deferred_start(base: &goap_domain::BaselineID) -> StartRequest {
    StartRequest {
        methodology: "deferred-bind".into(),
        agent: "intake".into(),
        goal: "bound".into(),
        baseline_id: base.clone(),
        project_id: TEST_PROJECT.into(),
        ..Default::default()
    }
}

/// The default (eager) path: a process binds its change immediately, before Run's own first cycle, and that bind is
/// visible as its own journal.attach record (ADR 0031, gap 5).
#[tokio::test]
async fn eager_bind_journals_attach() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base,
                project_id: TEST_PROJECT.into(),
                intent: PSP.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        !p.change_id.is_empty() && p.status == STATUS_RUNNING,
        "eager bind: process = {p:?}"
    );
    let recs: Vec<_> = attaches(&*g, &p.change_id, &p.id)
        .await
        .into_iter()
        .filter(|r| r.kind == journal::KIND_ATTACH)
        .collect();
    assert_eq!(
        recs.len(),
        1,
        "expected exactly one attach record: {recs:?}"
    );
    assert!(
        recs[0].data.get("reused") == Some(&Value::Bool(false))
            && recs[0].data.get("changeId") == Some(&Value::from(p.change_id.to_string())),
        "attach record: {:?}",
        recs[0]
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "run: {p:?}");
}

/// An agent that declares its own change_bound-effect action defers the bind: the process starts and plans with no
/// change at all, and only becomes bound once that action (here, a direct attach_change call) runs.
#[tokio::test]
async fn deferred_bind_starts_unbound() {
    let ctx = ctx();
    let mut s = setup().await;
    s.add(load_methodology("deferred-bind.yaml"));
    let (e, _g, base, _errs) = s.share();
    let mut p = e.start(&ctx, deferred_start(&base)).await.unwrap();
    assert!(
        p.change_id.is_empty() && p.status == STATUS_RUNNING,
        "expected an unbound, running process, got {p:?}"
    );
    let m = e
        .methodologies
        .methodology(&ctx, "deferred-bind")
        .await
        .unwrap();
    let bb = goap_engine::Engine::internals_of(&e)
        .observe(&ctx, &mut p, &m)
        .await
        .unwrap();
    assert!(
        bb.change.id.is_empty() && bb.nodes.is_empty(),
        "unbound process should see no change-backed blackboard, got {bb:?}"
    );
    assert!(
        !p.world.get("change_bound").copied().unwrap_or_default(),
        "change_bound should be false while unbound: {:?}",
        p.world
    );

    e.attach_change(&ctx, &p.id, AttachRequest::default())
        .await
        .unwrap();
    let p = get(&e, &p.id).await;
    assert!(
        !p.change_id.is_empty(),
        "attach_change should have bound a change: {p:?}"
    );
    let recs: Vec<_> = attaches(&*e.graph, &p.change_id, &p.id)
        .await
        .into_iter()
        .filter(|r| r.kind == journal::KIND_ATTACH)
        .collect();
    assert!(
        recs.len() == 1 && recs[0].data.get("reused") == Some(&Value::Bool(false)),
        "expected exactly one non-reused attach record, got {recs:?}"
    );
    // change_bound is now true and the goal was only ever that: the process completes on its very next cycle
    // without replanning "bind".
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "run after attach: {p:?}");
}

/// Reusing an existing change is the other attach_change path.
#[tokio::test]
async fn attach_change_reuses_existing_change() {
    let ctx = ctx();
    let mut s = setup().await;
    s.add(load_methodology("deferred-bind.yaml"));
    let (e, g, base, _errs) = s.share();
    let existing = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "existing".into(),
                intent: "pre-existing change to reuse".into(),
                namespace: "alm".into(),
                baseline_id: base.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.start(&ctx, deferred_start(&base)).await.unwrap();
    e.attach_change(
        &ctx,
        &p.id,
        AttachRequest {
            change_id: existing.id.clone(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let p = get(&e, &p.id).await;
    assert_eq!(p.change_id, existing.id, "bound to the existing change");
    let recs = attaches(&*g, &existing.id, &p.id).await;
    assert!(
        recs.len() == 1
            && recs[0].kind == journal::KIND_ATTACH
            && recs[0].data.get("reused") == Some(&Value::Bool(true)),
        "expected one reused attach record, got {recs:?}"
    );
}

/// A change is never created without a project (ADR 0091): a run naming none works in the root project, and a run
/// on an existing change works where the change does, also after the change moved.
#[tokio::test]
async fn start_project_of_the_change() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let st = Port::structures(&*g, &ctx).await.unwrap();
    let root = st.project().root;
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base.clone(),
                intent: PSP.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert!(
        c.project_id == root && p.project == root,
        "no project: change in {:?}, run in {:?}",
        c.project_id,
        p.project
    );
    let own = g
        .create_change(
            &ctx,
            NewChange {
                title: "t".into(),
                namespace: c.namespace.clone(),
                baseline_id: base,
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let mut q = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                change_id: own.id.clone(),
                project_id: "PROJ-OTHER".into(),
                intent: "what does this break?".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        q.project, TEST_PROJECT,
        "a run on a change works in its project"
    );
    graphtest::project(&ctx, &g, "PROJ-MOVED", "Moved")
        .await
        .unwrap();
    g.move_change(&ctx, &own.id, "PROJ-MOVED").await.unwrap();
    let m = e
        .methodologies
        .methodology(&ctx, "impact-analysis")
        .await
        .unwrap();
    goap_engine::Engine::internals_of(&e)
        .observe(&ctx, &mut q, &m)
        .await
        .unwrap();
    assert_eq!(
        q.project, "PROJ-MOVED",
        "a run follows the move of its change"
    );
}
