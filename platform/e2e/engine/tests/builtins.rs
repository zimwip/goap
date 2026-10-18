//! Go: builtins_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_builtins as builtins;
use goap_ctx::Ctx;
use goap_domain::{
    self as domain, Baseline, Blackboard, Branch, Change, ChangeID, ChangeImpact, ChangeStatus,
    NodeID,
};
use goap_engine::{
    ActionContext, ExecResult, HumanTask, Internals, MemoryStore, Process, RebaseConflict,
    RebaseTask, STATUS_RUNNING, STATUS_WAITING, StaticMethodologies, Step, TASK_MERGE, TASK_REBASE,
    apply_change, builtin, default_builtins,
};
use goap_graphapi::{Error, ImpactConflicts, MergeCandidate, MergePlan, RebaseState, Resolution};
use goap_methodology::Action;
use parking_lot::Mutex;
use serde_json::{Value, json};

use crate::common::stub::{Stub, StubGraph};
use crate::common::*;

#[test]
fn builtin_register() {
    let b = default_builtins();
    for n in [
        builtins::GRAPH_PROPAGATE,
        builtins::GRAPH_APPLY,
        builtins::DECISION_INVESTIGATE,
    ] {
        assert!(b.has_builtin(n), "{n} not registered");
    }
    assert!(
        !b.has_builtin(builtins::OBSERVE_ANALYZE),
        "the engine registers no self-improvement builtin"
    );
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(move || {
        let mut b = b;
        b.register(
            builtins::GRAPH_APPLY,
            builtin(|ctx, ac| async move { apply_change(&ctx, &ac).await }),
        );
    }));
    assert!(r.is_err(), "a duplicate registration must panic");
}

/// ApplyStub is a graph whose Apply left the change in the status given, and whose merge into main has one
/// conflict: both branches changed the title.
#[derive(Default)]
struct ApplyStub {
    status: Mutex<ChangeStatus>,
    integrated: Mutex<BTreeMap<NodeID, Resolution>>,
}

#[async_trait]
impl Stub for ApplyStub {
    async fn apply(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<Baseline, Error> {
        Ok(Baseline {
            id: "b1".into(),
            name: "n".into(),
            ..Default::default()
        })
    }
    async fn change(&self, _: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        Ok(Change {
            id: id.clone(),
            namespace: "alm".into(),
            branch: "change-ab12cd34".into(),
            status: self.status.lock().clone(),
            ..Default::default()
        })
    }
    async fn branch(&self, _: &Ctx, ns: &str, name: &str) -> Result<Branch, Error> {
        Ok(Branch {
            name: name.into(),
            namespace: ns.into(),
            parent: "main".into(),
            ..Default::default()
        })
    }
    async fn plan_merge(
        &self,
        _: &Ctx,
        _: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan, Error> {
        Ok(MergePlan {
            from: from.into(),
            into: into.into(),
            candidates: vec![
                MergeCandidate {
                    node: "n1".into(),
                    key: "REQ-1".into(),
                    kind: "merge".into(),
                    conflicts: vec!["title".into()],
                    base: obj(json!({"title": "a"})),
                    ours_props: obj(json!({"title": "b"})),
                    theirs_props: obj(json!({"title": "c"})),
                    ..Default::default()
                },
                // merged by itself: not listed
                MergeCandidate {
                    node: "n2".into(),
                    key: "REQ-2".into(),
                    kind: "merge".into(),
                    merged: obj(json!({"title": "x"})),
                    ..Default::default()
                },
            ],
            ..Default::default()
        })
    }
    async fn integrate_change(
        &self,
        _: &Ctx,
        id: &ChangeID,
        rs: BTreeMap<NodeID, Resolution>,
    ) -> Result<Change, Error> {
        *self.integrated.lock() = rs;
        *self.status.lock() = domain::CHANGE_APPLIED;
        Ok(Change {
            id: id.clone(),
            status: domain::CHANGE_APPLIED,
            ..Default::default()
        })
    }
}

fn action_context(g: Arc<dyn goap_engine::GraphPort>) -> ActionContext {
    ActionContext {
        process: Arc::new(Process::default()),
        action: Action::default(),
        blackboard: Blackboard {
            change: Change {
                id: "c1".into(),
                ..Default::default()
            },
            ..Default::default()
        },
        graph: g,
        host: None,
        step: None,
        form: None,
    }
}

#[tokio::test]
async fn apply_change_asks_for_the_conflicts_of_the_merge() {
    let run = |st: ChangeStatus| async move {
        let g = Arc::new(StubGraph(ApplyStub {
            status: Mutex::new(st),
            ..Default::default()
        }));
        apply_change(&ctx(), &action_context(g)).await
    };
    let r = run(domain::CHANGE_COMMITTED)
        .await
        .map_err(|e| e.error)
        .unwrap();
    let m = r
        .merge
        .as_ref()
        .expect("a committed change waits for its merge");
    assert!(
        m.from == "change-ab12cd34" && m.into == "main" && m.conflicts.len() == 1,
        "the merge task: {m:?}"
    );
    let pr = &m.conflicts[0].properties[0];
    assert!(
        pr.name == "title"
            && pr.ours == "b"
            && pr.theirs == "c"
            && pr.proposed == "c"
            && pr.base == "a",
        "both values and the proposal are shown: {pr:?}"
    );
    let r = run(domain::CHANGE_APPLIED)
        .await
        .map_err(|e| e.error)
        .unwrap();
    assert!(
        r.merge.is_none() && r.output.contains("created"),
        "an applied change: {r:?}"
    );
}

#[tokio::test]
async fn resolve_merge_completes_the_merge_and_resumes() {
    let ctx = ctx();
    let stub = Arc::new(StubGraph(ApplyStub {
        status: Mutex::new(domain::CHANGE_COMMITTED),
        ..Default::default()
    }));
    let task = Internals::merge_task_of(
        &ctx,
        &*stub,
        &Change {
            id: "c1".into(),
            namespace: "alm".into(),
            branch: "change-ab12cd34".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let e = Arc::new(goap_engine::Engine::new(
        stub.clone(),
        Arc::new(StaticMethodologies::new()),
        Arc::new(MemoryStore::new()),
    ));
    let p = Process {
        id: "p1".into(),
        change_id: "c1".into(),
        status: STATUS_WAITING.into(),
        steps: vec![Step {
            index: 0,
            action: "apply".into(),
            ..Default::default()
        }],
        pending: Some(HumanTask {
            kind: TASK_MERGE.into(),
            action: "apply".into(),
            step: 0,
            merge: Some(task),
            ..Default::default()
        }),
        ..Default::default()
    };
    e.store.put(&ctx, &p).await.unwrap();
    assert!(
        e.resolve_merge(&ctx, "p1", BTreeMap::new(), "")
            .await
            .is_err(),
        "every conflict needs a resolution"
    );
    let got = e
        .resolve_merge(
            &ctx,
            "p1",
            BTreeMap::from([(
                NodeID::from("n1"),
                Resolution {
                    props: obj(json!({"title": "c"})),
                    ..Default::default()
                },
            )]),
            "ok",
        )
        .await
        .unwrap();
    let integrated = stub.0.integrated.lock().clone();
    assert!(
        got.status == STATUS_RUNNING
            && got.pending.is_none()
            && integrated
                .get(&NodeID::from("n1"))
                .and_then(|r| r.props.get("title"))
                == Some(&Value::from("c")),
        "the merge completes and the run goes on: {got:?} {integrated:?}"
    );
    assert!(
        e.resolve_merge(&ctx, "p1", BTreeMap::new(), "")
            .await
            .is_err(),
        "nothing pending any more"
    );
}

/// RebaseStub is a graph whose Apply stops on the rebase: conflicts left to settle, impacts still to review.
#[derive(Default)]
struct RebaseStub {
    apply_err: Option<Error>,
    conflicts: Mutex<Vec<ImpactConflicts>>,
    proposed: Mutex<bool>,
}

#[async_trait]
impl Stub for RebaseStub {
    async fn apply(&self, _: &Ctx, _: &ChangeID, _: &str) -> Result<Baseline, Error> {
        match &self.apply_err {
            Some(e) => Err(e.clone()),
            None => Ok(Baseline::default()),
        }
    }
    async fn rebase_state(&self, _: &Ctx, _: &ChangeID) -> Result<RebaseState, Error> {
        Ok(RebaseState {
            branch: "main".into(),
            conflicts: self.conflicts.lock().clone(),
            ..Default::default()
        })
    }
    async fn change(&self, _: &Ctx, id: &ChangeID) -> Result<Change, Error> {
        let review = if *self.proposed.lock() {
            domain::REVIEW_PROPOSED
        } else {
            domain::REVIEW_ACCEPTED
        };
        Ok(Change {
            id: id.clone(),
            status: domain::CHANGE_ACTIVE,
            nodes: vec![ChangeImpact {
                id: "i1".into(),
                key: "REQ-1".into(),
                review,
                ..Default::default()
            }],
            ..Default::default()
        })
    }
}

fn conflicts(impact: &str, key: &str, cs: &[&str]) -> Vec<ImpactConflicts> {
    vec![ImpactConflicts {
        impact: impact.into(),
        key: key.into(),
        conflicts: cs.iter().map(|c| c.to_string()).collect(),
    }]
}

#[tokio::test]
async fn apply_change_waits_on_the_conflicts_of_the_rebase() {
    let conflict = Error::wrap(goap_graphapi::ErrorKind::Conflict, "change c1 rebased");
    let run = |s: RebaseStub| async move {
        apply_change(&ctx(), &action_context(Arc::new(StubGraph(s)))).await
    };
    let r: ExecResult = run(RebaseStub {
        apply_err: Some(conflict.clone()),
        conflicts: Mutex::new(conflicts("i1", "REQ-1", &["props.title", "owner"])),
        ..Default::default()
    })
    .await;
    let r = r.map_err(|e| e.error).unwrap();
    let rb = r.rebase.as_ref().expect("a rebase conflict is a task");
    assert!(
        rb.branch == "main"
            && rb.impacts.len() == 1
            && rb.impacts[0].key == "REQ-1"
            && rb.impacts[0].conflicts.len() == 2,
        "the task: {rb:?}"
    );
    // a conflict without any rebase conflict (impacts awaiting their review, say) stays an error
    let err = run(RebaseStub {
        apply_err: Some(conflict),
        ..Default::default()
    })
    .await
    .unwrap_err();
    assert!(
        err.error.is_conflict() && err.partial.rebase.is_none(),
        "an ordinary conflict: {}",
        err.error
    );
    assert!(
        run(RebaseStub {
            apply_err: Some(Error::msg("boom")),
            conflicts: Mutex::new(conflicts("i1", "", &[])),
            ..Default::default()
        })
        .await
        .is_err(),
        "another error stays an error"
    );
}

#[tokio::test]
async fn resolve_rebase_needs_the_conflicts_settled_and_reviewed() {
    let ctx = ctx();
    let stub = Arc::new(StubGraph(RebaseStub {
        conflicts: Mutex::new(conflicts("i1", "REQ-1", &["props.title"])),
        ..Default::default()
    }));
    let e = Arc::new(goap_engine::Engine::new(
        stub.clone(),
        Arc::new(StaticMethodologies::new()),
        Arc::new(MemoryStore::new()),
    ));
    let p = Process {
        id: "p1".into(),
        change_id: "c1".into(),
        status: STATUS_WAITING.into(),
        steps: vec![Step {
            index: 0,
            action: "apply".into(),
            ..Default::default()
        }],
        pending: Some(HumanTask {
            kind: TASK_REBASE.into(),
            action: "apply".into(),
            step: 0,
            rebase: Some(RebaseTask {
                branch: "main".into(),
                impacts: vec![RebaseConflict {
                    impact: "i1".into(),
                    key: "REQ-1".into(),
                    conflicts: vec!["props.title".into()],
                }],
                ..Default::default()
            }),
            ..Default::default()
        }),
        ..Default::default()
    };
    e.store.put(&ctx, &p).await.unwrap();
    let err = e.resolve_rebase(&ctx, "p1", "").await.unwrap_err();
    assert!(
        err.is_invalid_state() && err.to_string().contains("REQ-1"),
        "a conflict remains: {err}"
    );
    stub.0.conflicts.lock().clear();
    *stub.0.proposed.lock() = true;
    let err = e.resolve_rebase(&ctx, "p1", "").await.unwrap_err();
    assert!(
        err.is_invalid_state() && err.to_string().contains("review"),
        "an impact awaits its review: {err}"
    );
    *stub.0.proposed.lock() = false;
    let got = e.resolve_rebase(&ctx, "p1", "ok").await.unwrap();
    assert!(
        got.status == STATUS_RUNNING && got.pending.is_none(),
        "the run goes on: {got:?}"
    );
    let err = e.resolve_rebase(&ctx, "p1", "").await.unwrap_err();
    assert!(err.is_invalid_state(), "nothing pending any more: {err}");
}
