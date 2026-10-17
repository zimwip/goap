//! The parts of activitygoals_test.go that need no graph: the activity goal of a change evaluated by the registry
//! (Service::merge, activity_goals_met), on blackboards made by hand. The gate at Apply is in changelifecycle.rs.

use std::sync::Arc;

use goap_domain::{Blackboard, Change, ChangeItem, ITEM_ACCEPTED, TYPE_ARTIFACT};
use goap_methodology::{Condition, Methodology, Process, Step};

use super::{FakeGraph, bg};
use crate::activitygoals::{DATA_ACTIVITY, activity_of};
use crate::graphstore::{GraphStore, methodology_version_key};
use crate::service::Service;
use crate::store::{Record, STATUS_PUBLISHED, Store};

async fn shipping() -> (Service, String) {
    let ctx = bg();
    let store = Arc::new(GraphStore::new(FakeGraph::new()));
    let reg = Service::new(store.clone());
    let m = Methodology {
        name: "shipping".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "signed".into(),
            expr: r#"artifacts.exists(a, a.target == "signoff")"#.into(),
            ..Condition::default()
        }],
        processes: vec![Process {
            name: "ship".into(),
            steps: vec![Step {
                name: "confirm".into(),
                done: [("signed".to_string(), true)].into_iter().collect(),
                ..Step::default()
            }],
            ..Process::default()
        }],
        ..Methodology::default()
    };
    store
        .save(
            &ctx,
            Record {
                methodology: m.clone(),
                status: STATUS_PUBLISHED,
                ..Record::default()
            },
        )
        .await
        .expect("save");
    (
        reg,
        format!(
            "{}/process/ship",
            methodology_version_key(&m.name, &m.version)
        ),
    )
}

fn artifact(target: &str) -> ChangeItem {
    ChangeItem {
        r#type: TYPE_ARTIFACT,
        target: target.into(),
        status: ITEM_ACCEPTED,
        ..ChangeItem::default()
    }
}

fn scoped(r: &str, items: Vec<ChangeItem>) -> Blackboard {
    let mut c = Change::default();
    c.data.insert(DATA_ACTIVITY.into(), r.into());
    c.items = items;
    Blackboard {
        change: c,
        ..Blackboard::default()
    }
}

// A change scoped to a process is decided by the guardian of its merge: the goal of that process.
#[tokio::test]
async fn activity_goals_met_decides_the_merge() {
    let (reg, r) = shipping().await;
    let ctx = bg();
    let bb = scoped(&r, vec![]);
    assert_eq!(activity_of(&bb.change), r);
    assert_eq!(
        reg.merge(&ctx, &bb.change, &bb).await.expect("merge"),
        (true, false),
        "goal unmet"
    );
    let bb = scoped(&r, vec![artifact("note")]);
    assert_eq!(
        reg.merge(&ctx, &bb.change, &bb).await.expect("merge"),
        (true, false),
        "an unrelated artifact still does not satisfy it"
    );
    let bb = scoped(&r, vec![artifact("signoff")]);
    assert_eq!(
        reg.merge(&ctx, &bb.change, &bb).await.expect("merge"),
        (true, true),
        "goal met"
    );
    // a change scoped to no activity is not decided
    let bb = Blackboard::default();
    assert_eq!(
        reg.merge(&ctx, &bb.change, &bb).await.expect("merge"),
        (false, false)
    );
}

// An unresolvable activity reference is a hard error, not a silent pass.
#[tokio::test]
async fn activity_goals_met_refuses_an_unresolvable_ref() {
    let (reg, _) = shipping().await;
    let ctx = bg();
    for r in [
        "MV:no-such@1/process/p",
        "MV:shipping@1/process/nope",
        "not-an-activity",
    ] {
        let bb = scoped(r, vec![]);
        assert!(
            reg.merge(&ctx, &bb.change, &bb).await.is_err(),
            "{r}: an unresolvable activity ref must refuse"
        );
    }
}
