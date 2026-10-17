//! Go: startingpoints_test.go.

use std::sync::Arc;

use goap_authz::Principal;
use goap_domain::{self as domain, ChangeID};
use goap_engine::{
    Engine, ItemInput, POINT_METHOD, POINT_STEP, STATUS_WAITING, StartRequest, StartingPoints,
    StartingPointsOptions,
};
use goap_graph::{ChangePatch, Graph, NewChange};
use goap_startingpoint as startingpoint;

use crate::common::*;
use crate::steps::staged_setup;

fn point_ids(sp: &StartingPoints) -> Vec<String> {
    sp.points.iter().map(|p| p.id.clone()).collect()
}

/// composed reads the points in the view of the methodology version they name, as their readers do.
async fn composed(e: &Engine, sp: &StartingPoints) -> startingpoint::Points {
    let c = e
        .methodologies
        .methodology(
            &ctx(),
            &domain::methodology_ref(&sp.methodology, &sp.version),
        )
        .await
        .unwrap();
    let v = goap_registrysvc::methodology_view_to_pb(&c.view());
    startingpoint::compose(Some(&v), &goap_enginesvc::starting_points_to_pb(sp))
}

async fn point_change(g: &Graph, mut input: NewChange) -> ChangeID {
    input.project_id = TEST_PROJECT.into();
    input.title = "points".into();
    g.create_change(&ctx(), input).await.unwrap().id
}

async fn staged() -> (Arc<Engine>, Arc<Graph>, Errs) {
    let (e, g, _, errs) = staged_setup().await.share();
    (e, g, errs)
}

/// Only the steps whose entry conditions all hold are proposed, as steps and methods (never actions); the others are
/// reported as blocked, and a step carried out or done leaves the list (ADR 0097).
#[tokio::test]
async fn starting_points_are_possible_steps_only() {
    let ctx = as_who(&Principal {
        subject: "w".into(),
        roles: vec!["writer@ORG-DEFAULT".into()],
        ..Default::default()
    });
    let (e, g, _errs) = staged().await;
    let id = point_change(
        &g,
        NewChange {
            methodology: "staged".into(),
            goal: "delivery".into(),
            ..Default::default()
        },
    )
    .await;
    let opts = StartingPointsOptions::default;

    let sp = e.starting_points(&ctx, &id, opts()).await.unwrap();
    // "prepare" is possible as a whole step (its sub-step "note" is the scheduler's business); "verify" needs the
    // note and "approve" the check: not proposed, however ready their actions would be
    assert_eq!(point_ids(&sp), ["delivery/prepare"], "points");
    let pt = sp.points[0].clone();
    assert!(
        pt.kind == POINT_STEP
            && pt.launch.agent == "delivery"
            && pt.launch.goal == "delivery/prepare"
            && pt.launch.change_id == id
            && pt.launch.methodology == "staged"
            && pt.may_run
            && !pt.running,
        "point {pt:?}"
    );
    // what the step is comes from the view of the methodology version the points name (ADR 0126 step 5)
    let read = composed(&e, &sp).await.points.remove(0);
    assert!(
        read.name == "prepare"
            && read.process == "delivery"
            && read.responsible == "writer"
            && read.accountable == "reviewer",
        "composed point {read:?}"
    );
    assert_eq!(sp.blocked_count, 2, "blocked {:?}", sp.blocked);
    for b in &sp.blocked {
        assert!(
            !((b.id == "delivery/verify" && b.missing != ["noted"])
                || (b.id == "delivery/approve" && b.missing != ["checked"])),
            "blocked {b:?}"
        );
    }

    // starting the point runs the step alone: the goal is its exit, the run carries the step
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: pt.launch.methodology.clone(),
                agent: pt.launch.agent.clone(),
                goal: pt.launch.goal.clone(),
                change_id: pt.launch.change_id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        p.step
            .as_ref()
            .is_some_and(|s| s.path == "delivery/prepare"),
        "the run carries the step: {:?}",
        p.step
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.action == "delivery/prepare/note"),
        "run {p:?}"
    );
    let sp = e.starting_points(&ctx, &id, opts()).await.unwrap();
    assert!(
        sp.points.len() == 1 && sp.points[0].running,
        "a step carried out is running: {:?}",
        sp.points
    );

    // the note is written: the step is done and leaves; verify is now possible, through the method that applies
    e.submit(
        &ctx,
        &p.id,
        vec![ItemInput {
            r#type: "artifact".into(),
            target: "note".into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    schedule(&e, &p.id).await;
    let sp = e.starting_points(&ctx, &id, opts()).await.unwrap();
    assert_eq!(
        point_ids(&sp),
        ["delivery/verify"],
        "points after the note ({})",
        sp.reason
    );
    let pt = &sp.points[0];
    assert!(
        pt.kind == POINT_METHOD
            && pt.method == "peer_check"
            && pt.launch.agent == "peer_check"
            && pt.launch.goal == "peer_check"
            && pt.why == ["noted"],
        "method point {pt:?}"
    );
    let read = composed(&e, &sp).await;
    assert!(
        read.points[0].capability == "verification" && read.blocked[0].name == "approve",
        "composed method point {read:?}"
    );
    // capped blocked list
    let sp = e
        .starting_points(
            &ctx,
            &id,
            StartingPointsOptions {
                max_blocked: 0,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        sp.blocked_count == 1 && sp.blocked.len() == 1 && sp.blocked[0].id == "delivery/approve",
        "blocked {:?}",
        sp.blocked
    );
    let sp = e
        .starting_points(
            &ctx,
            &id,
            StartingPointsOptions {
                max_points: 0,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(sp.points.len(), 1, "default cap");
}

#[tokio::test]
async fn starting_points_without_methodology() {
    let ctx = ctx();
    let (e, g, _errs) = staged().await;
    let id = point_change(&g, NewChange::default()).await;
    let sp = e
        .starting_points(&ctx, &id, StartingPointsOptions::default())
        .await
        .unwrap();
    assert!(
        sp.points.is_empty() && !sp.reason.is_empty(),
        "no methodology: {sp:?}"
    );
    // a methodology chosen for the change: its main goal pulls the scheduler
    let sp = e
        .starting_points(
            &ctx,
            &id,
            StartingPointsOptions {
                methodology: "staged".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        !sp.points.is_empty() && !sp.goal.is_empty(),
        "chosen methodology: {sp:?}"
    );
}

/// A change with no goal of its own (created before the default goal) is pulled by the main goal of its methodology.
#[tokio::test]
async fn starting_points_fall_back_to_the_main_goal() {
    let ctx = ctx();
    let (e, g, _errs) = staged().await;
    let id = point_change(
        &g,
        NewChange {
            methodology: "staged".into(),
            ..Default::default()
        },
    )
    .await;
    g.update_change(
        &ctx,
        &id,
        &ChangePatch {
            goal: Some(String::new()),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let sp = e
        .starting_points(&ctx, &id, StartingPointsOptions::default())
        .await
        .unwrap();
    assert!(
        !sp.points.is_empty() && !sp.goal.is_empty() && sp.reason.is_empty(),
        "no goal: {sp:?}"
    );
}

/// The shipped sdlc methodology: its main goal `deliver` is the end point; at the start only the framing of the
/// change is possible, every other phase waits for conditions.
#[tokio::test]
async fn starting_points_of_sdlc() {
    let ctx = ctx();
    let mut s = setup().await;
    s.add(load_methodology("sdlc.yaml"));
    let (e, g, _, _errs) = s.share();
    let id = point_change(
        &g,
        NewChange {
            methodology: "sdlc".into(),
            goal: "deliver".into(),
            ..Default::default()
        },
    )
    .await;
    let sp = e
        .starting_points(&ctx, &id, StartingPointsOptions::default())
        .await
        .unwrap();
    // the framing, whose automatic review rates the description (ADR 0110, 0130), is where the change starts
    assert_eq!(
        point_ids(&sp),
        ["software_delivery/framing"],
        "points ({}) blocked {:?}",
        sp.reason,
        sp.blocked
    );
    assert!(sp.blocked_count != 0, "the other phases wait");
    assert!(
        sp.points[0].launch.agent == "software_delivery"
            && sp.points[0].launch.goal == "software_delivery/framing",
        "{:?}",
        sp.points[0]
    );
}
