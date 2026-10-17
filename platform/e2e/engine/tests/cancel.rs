//! Go: cancel_test.go.

use std::sync::Arc;
use std::time::Duration;

use goap_ctx::{Ctx, CtxError};
use goap_engine::{
    HumanTask, Process, STATUS_CANCELLED, STATUS_COMPLETED, STATUS_WAITING, StartRequest,
    TASK_AGENT,
};
use goap_llm as llm;
use parking_lot::Mutex;
use tokio::sync::Notify;

use crate::common::*;
use crate::engine::deliver_until_reviewed;
use crate::mcp::{FakeHub, alice_in, docs_as_llm, mcp_engine};

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cancel_interrupts_a_run_in_flight() {
    let hub = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let called = Arc::new(Notify::new());
    let seen: Arc<Mutex<Option<CtxError>>> = Arc::default();
    let (c2, s2) = (called.clone(), seen.clone());
    let client = llm::ClientFunc::shared(move |ctx: Ctx, _r: llm::Request| {
        let (called, seen) = (c2.clone(), s2.clone());
        async move {
            called.notify_one();
            let err = ctx.done().await; // a long model call: it ends with its context
            *seen.lock() = Some(err);
            Err::<llm::Response, _>(llm::Error::Ctx(err))
        }
    });
    let (e, base, _errs) = mcp_engine(hub, Some(client), Some(&docs_as_llm)).await;
    let alice = alice_in("acme");
    let p = e
        .start(
            &alice,
            StartRequest {
                methodology: "docs".into(),
                agent: "writer".into(),
                goal: "done".into(),
                baseline_id: base.clone(),
                intent: "document it".into(),
                owner_org: "acme".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let done = {
        let (e, alice, id) = (e.clone(), alice.clone(), p.id.clone());
        tokio::spawn(async move { e.run(&alice, &id).await })
    };
    called.notified().await;
    let c = e.cancel(&alice, &p.id, "wrong change").await.unwrap();
    tokio::time::timeout(Duration::from_secs(5), done)
        .await
        .expect("the run did not stop")
        .unwrap()
        .ok();
    let seen = *seen.lock();
    assert!(
        c.status == STATUS_CANCELLED
            && c.error == "cancelled by alice: wrong change"
            && seen == Some(CtxError::Canceled),
        "cancelled: {} {:?}, the model call saw {seen:?}",
        c.status,
        c.error
    );
    let stored = get(&e, &p.id).await;
    assert!(
        stored.status == STATUS_CANCELLED && stored.pending.is_none(),
        "stored {stored:?}"
    );
    // it stays cancelled: a later run does nothing, a second cancellation is refused
    let again = e.run(&alice, &p.id).await.unwrap();
    assert_eq!(again.status, STATUS_CANCELLED, "run after cancel");
    let err = e.cancel(&alice, &p.id, "").await.unwrap_err();
    assert!(err.is_invalid_state(), "second cancel: {err}");
}

#[tokio::test]
async fn cancel_a_waiting_run_only_by_who_answers_for_it() {
    let (e, _g, p, _errs) = deliver_until_reviewed(contributor()).await;
    let other = as_who(&principal("dave", "globex", &[]));
    let err = e.cancel(&other, &p.id, "no").await.unwrap_err();
    assert!(err.is_forbidden(), "forbidden: {err}");
    let c = e.cancel(&as_who(&contributor()), &p.id, "").await.unwrap();
    assert!(
        c.status == STATUS_CANCELLED
            && c.pending.is_none()
            && c.error
                .starts_with(&format!("cancelled by {}", contributor().subject)),
        "cancel: {c:?}"
    );
}

#[tokio::test]
async fn cancel_takes_the_live_sub_processes() {
    let (e, _, _, _errs) = setup().await.share();
    let alice = principal("alice", "", &[]);
    let ctx = as_who(&alice);
    let proc_ = |id: &str, parent: &str, status: &str| Process {
        id: id.into(),
        parent_id: parent.into(),
        status: status.into(),
        initiator: alice.clone(),
        ..Default::default()
    };
    let mut parent = proc_("parent", "", STATUS_WAITING);
    parent.pending = Some(HumanTask {
        kind: TASK_AGENT.into(),
        child_process_id: "child".into(),
        ..Default::default()
    });
    for p in [
        parent,
        proc_("child", "parent", STATUS_WAITING),
        proc_("done", "parent", STATUS_COMPLETED),
    ] {
        e.store.put(&ctx, &p).await.unwrap();
    }
    e.cancel(&ctx, "parent", "stop").await.unwrap();
    e.drain().await;
    for (id, want) in [
        ("parent", STATUS_CANCELLED),
        ("child", STATUS_CANCELLED),
        ("done", STATUS_COMPLETED),
    ] {
        let p = get(&e, id).await;
        assert_eq!(p.status, want, "{id}");
    }
    let c = get(&e, "child").await;
    assert_eq!(
        c.error, "cancelled by alice: its parent process was cancelled",
        "child error"
    );
}
