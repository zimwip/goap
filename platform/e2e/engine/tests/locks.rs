//! Go: locks_test.go.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicI32, Ordering};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeItem};
use goap_engine::{
    Engine, Locker, MemoryStore, OrphanFinder, Process, ProcessLogEntry, STATUS_CANCELLED,
    STATUS_COMPLETED, STATUS_RUNNING, Signals, StartRequest, Step, Store, ToolPort,
};
use goap_journal as journal;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::json;
use tokio::sync::{Notify, mpsc};

use crate::common::*;
use crate::llmloop::DOC_ITEMS;
use crate::mcp::{FakeHub, alice_in, docs_as_llm, mcp_setup, parse_docs};

/// SharedLocks is the lock service of several replicas in memory (the SQL store's twin, ADR 0124).
#[derive(Default)]
pub struct SharedLocks {
    locks: Mutex<HashMap<String, SharedLock>>,
}

struct SharedLock {
    holder: String,
    interrupt: String,
    until: Instant,
}

/// Replica is the view of one replica on the shared locks.
struct Replica {
    s: Arc<SharedLocks>,
    holder: String,
}

#[async_trait]
impl Locker for Replica {
    async fn acquire(
        &self,
        _: &Ctx,
        key: &str,
        ttl: Duration,
    ) -> goap_engine::Result<(bool, String)> {
        let mut locks = self.s.locks.lock();
        let now = Instant::now();
        match locks.get_mut(key) {
            Some(l) if l.until > now && l.holder == self.holder => {
                l.until = now + ttl;
                Ok((true, l.interrupt.clone()))
            }
            Some(l) if l.until > now => Ok((false, String::new())),
            _ => {
                locks.insert(
                    key.to_string(),
                    SharedLock {
                        holder: self.holder.clone(),
                        interrupt: String::new(),
                        until: now + ttl,
                    },
                );
                Ok((true, String::new()))
            }
        }
    }

    async fn release(&self, _: &Ctx, key: &str) -> goap_engine::Result<()> {
        let mut locks = self.s.locks.lock();
        if locks.get(key).is_some_and(|l| l.holder == self.holder) {
            locks.remove(key);
        }
        Ok(())
    }

    async fn interrupt(&self, _: &Ctx, key: &str, by: &str) -> goap_engine::Result<bool> {
        let mut locks = self.s.locks.lock();
        match locks.get_mut(key) {
            Some(l) if l.holder != self.holder && l.until > Instant::now() => {
                l.interrupt = by.to_string();
                Ok(true)
            }
            _ => Ok(false),
        }
    }
}

/// OrphanStore tells the running processes whose lock nobody holds (the SQL store's twin).
struct OrphanStore {
    m: Arc<MemoryStore>,
    locks: Arc<SharedLocks>,
}

#[async_trait]
impl Store for OrphanStore {
    async fn get(&self, ctx: &Ctx, id: &str) -> goap_engine::Result<Process> {
        self.m.get(ctx, id).await
    }
    async fn put(&self, ctx: &Ctx, p: &Process) -> goap_engine::Result<()> {
        self.m.put(ctx, p).await
    }
    async fn list(&self, ctx: &Ctx) -> goap_engine::Result<Vec<Process>> {
        self.m.list(ctx).await
    }
    async fn append_process_log(
        &self,
        ctx: &Ctx,
        process_id: &str,
        entries: Vec<ProcessLogEntry>,
    ) -> goap_engine::Result<()> {
        self.m.append_process_log(ctx, process_id, entries).await
    }
    async fn list_process_log(
        &self,
        ctx: &Ctx,
        process_id: &str,
    ) -> goap_engine::Result<Vec<ProcessLogEntry>> {
        self.m.list_process_log(ctx, process_id).await
    }
    fn orphan_finder(&self) -> Option<&dyn OrphanFinder> {
        Some(self)
    }
}

#[async_trait]
impl OrphanFinder for OrphanStore {
    async fn orphans(&self, ctx: &Ctx, before: DateTime<Utc>) -> goap_engine::Result<Vec<String>> {
        let all = self.m.list(ctx).await?;
        let locks = self.locks.locks.lock();
        let now = Instant::now();
        Ok(all
            .into_iter()
            .filter(|p| {
                p.status == STATUS_RUNNING
                    && p.updated_at < before
                    && locks.get(&p.id).is_none_or(|l| l.until <= now)
            })
            .map(|p| p.id)
            .collect())
    }
}

/// Replicas are two engines on one store and one lock service, running the docs methodology with a client; a is
/// the one start uses.
struct Replicas {
    a: Engine,
    b: Engine,
    base: domain::BaselineID,
    locks: Arc<SharedLocks>,
    _errs: Errs,
}

impl Replicas {
    /// share is the two engines, shared.
    fn share(
        self,
    ) -> (
        Arc<Engine>,
        Arc<Engine>,
        domain::BaselineID,
        Arc<SharedLocks>,
        Errs,
    ) {
        (
            Arc::new(self.a),
            Arc::new(self.b),
            self.base,
            self.locks,
            self._errs,
        )
    }
}

async fn two_replicas(client: Arc<dyn llm::Client>) -> Replicas {
    let hub: Arc<dyn ToolPort> = Arc::new(FakeHub::bound("acme", &["document-repository"]));
    let mut s = mcp_setup(hub, Some(client)).await;
    s.add(parse_docs(Some(&docs_as_llm)));
    let locks = Arc::new(SharedLocks::default());
    let store: Arc<dyn Store> = Arc::new(OrphanStore {
        m: s.store.clone(),
        locks: locks.clone(),
    });
    s.e.store = store.clone();
    s.e.locks = Some(Arc::new(Replica {
        s: locks.clone(),
        holder: "a".into(),
    }));
    s.e.lock_ttl = Duration::from_millis(200);
    let mut b = Engine::new(s.e.graph.clone(), s.e.methodologies.clone(), store);
    b.executors = s.e.executors.clone();
    b.scope = s.e.scope.clone();
    b.locks = Some(Arc::new(Replica {
        s: locks.clone(),
        holder: "b".into(),
    }));
    b.lock_ttl = s.e.lock_ttl;
    Replicas {
        a: s.e,
        b,
        base: s.base,
        locks,
        _errs: s.errs,
    }
}

fn alice() -> Ctx {
    alice_in("acme")
}

async fn start(a: &Arc<Engine>, base: &domain::BaselineID) -> Process {
    a.start(
        &alice(),
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
    .unwrap()
}

fn doc_client() -> Arc<dyn llm::Client> {
    text_client(DOC_ITEMS)
}

/// a client whose call waits for the end of its context, telling when it is called.
fn blocking_client(called: Arc<Notify>) -> Arc<dyn llm::Client> {
    llm::ClientFunc::shared(move |ctx: Ctx, _r: llm::Request| {
        let called = called.clone();
        async move {
            called.notify_one();
            let err = ctx.done().await;
            Err::<llm::Response, _>(llm::Error::Ctx(err))
        }
    })
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn replicas_run_a_process_once_at_a_time() {
    let calls = Arc::new(AtomicI32::new(0));
    let release = Arc::new(tokio::sync::Semaphore::new(0));
    let called = Arc::new(Notify::new());
    let client = {
        let (calls, release, called) = (calls.clone(), release.clone(), called.clone());
        llm::ClientFunc::shared(move |_ctx: Ctx, _r: llm::Request| {
            let (calls, release, called) = (calls.clone(), release.clone(), called.clone());
            async move {
                calls.fetch_add(1, Ordering::SeqCst);
                called.notify_one();
                let _ = release.acquire().await;
                Ok(llm::Response {
                    text: DOC_ITEMS.into(),
                    ..Default::default()
                })
            }
        })
    };
    let (a, b, base, _locks, _errs) = two_replicas(client).await.share();
    let p = start(&a, &base).await;
    let (tx, mut done) = mpsc::channel::<Option<Process>>(2);
    {
        let (a, id, tx) = (a.clone(), p.id.clone(), tx.clone());
        tokio::spawn(async move { tx.send(a.run(&alice(), &id).await.ok()).await });
    }
    called.notified().await;
    {
        let (b, id, tx) = (b.clone(), p.id.clone(), tx.clone());
        tokio::spawn(async move { tx.send(b.run(&alice(), &id).await.ok()).await });
    }
    tokio::time::sleep(Duration::from_millis(300)).await; // b waits for a's lock, renewed past its lease
    release.close();
    for _ in 0..2 {
        let got = tokio::time::timeout(Duration::from_secs(5), done.recv())
            .await
            .expect("a run did not end")
            .flatten();
        assert!(
            got.as_ref().is_some_and(|p| p.status == STATUS_COMPLETED),
            "run: {got:?}"
        );
    }
    let n = calls.load(Ordering::SeqCst);
    assert_eq!(
        n, 1,
        "model calls: the second replica ran the process again"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cancel_reaches_the_run_of_another_replica() {
    let called = Arc::new(Notify::new());
    let (a, b, base, _locks, _errs) = two_replicas(blocking_client(called.clone())).await.share();
    let p = start(&a, &base).await;
    let (tx, mut done) = mpsc::channel::<()>(1);
    {
        let (a, id) = (a.clone(), p.id.clone());
        tokio::spawn(async move {
            let _ = a.run(&alice(), &id).await;
            let _ = tx.send(()).await;
        });
    }
    called.notified().await;
    let c = b.cancel(&alice(), &p.id, "wrong change").await.unwrap();
    tokio::time::timeout(Duration::from_secs(5), done.recv())
        .await
        .expect("the run on the other replica did not stop");
    assert!(
        c.status == STATUS_CANCELLED && c.error == "cancelled by alice: wrong change",
        "cancelled: {} {:?}",
        c.status,
        c.error
    );
    let stored = a.store.get(&alice(), &p.id).await.unwrap();
    assert_eq!(stored.status, STATUS_CANCELLED, "stored {:?}", stored.error);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn recover_resumes_the_run_of_a_stopped_replica() {
    let (a, b, base, locks, _errs) = two_replicas(doc_client()).await.share();
    let p = start(&a, &base).await;
    // a replica took the process and stopped: its lease ends, nobody renews it
    locks.locks.lock().insert(
        p.id.clone(),
        SharedLock {
            holder: "gone".into(),
            interrupt: String::new(),
            until: Instant::now() + Duration::from_millis(100),
        },
    );
    let (ctx, cancel) = ctx().with_cancel();
    {
        let b = b.clone();
        tokio::spawn(async move { b.recover_loop(&ctx, Duration::from_millis(50)).await });
    }
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        let got = b.store.get(&alice(), &p.id).await.unwrap();
        if got.status == STATUS_COMPLETED {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "not resumed: {} {:?}",
            got.status,
            got.error
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    drop(cancel);
    b.drain().await;
}

#[derive(Clone, Copy)]
enum Wrote {
    Artifact,
    Record,
}

#[tokio::test]
async fn a_step_left_in_flight_is_not_written_twice() {
    const EXEC: &str = "exec-1";
    type Check = fn(&Process);
    let cases: [(&str, Vec<Wrote>, i32, Check); 3] = [
        ("nothing written: run again", vec![], 1, |p| {
            assert!(
                p.status == STATUS_COMPLETED && p.steps.len() == 1 && p.steps[0].error.is_empty(),
                "{} {:?}",
                p.status,
                p.steps
            );
        }),
        (
            "ended: closed as it is",
            vec![Wrote::Artifact, Wrote::Record],
            0,
            |p| {
                assert!(
                    p.status == STATUS_COMPLETED
                        && p.steps.len() == 1
                        && p.steps[0].error.is_empty()
                        && p.steps[0].ended_at.is_some(),
                    "{} {:?}",
                    p.status,
                    p.steps
                );
            },
        ),
        (
            "cut halfway: failed, not run again",
            vec![Wrote::Artifact],
            0,
            |p| {
                assert!(
                    !p.steps.is_empty()
                        && p.steps[0].error.contains("interrupted after writing part")
                        && p.disabled.get("fetch_doc").copied().unwrap_or_default(),
                    "{} {:?}",
                    p.status,
                    p.steps
                );
            },
        ),
    ];
    for (name, wrote, want_calls, check) in cases {
        let calls = Arc::new(AtomicI32::new(0));
        let client = {
            let calls = calls.clone();
            llm::ClientFunc::shared(move |_ctx: Ctx, _r: llm::Request| {
                calls.fetch_add(1, Ordering::SeqCst);
                async move {
                    Ok(llm::Response {
                        text: DOC_ITEMS.into(),
                        ..Default::default()
                    })
                }
            })
        };
        let (a, b, base, _locks, _errs) = two_replicas(client).await.share();
        let mut p = start(&a, &base).await;
        // a replica checkpointed the step and stopped while it ran
        p.steps.push(Step {
            action: "fetch_doc".into(),
            execution: EXEC.into(),
            started_at: Utc::now(),
            ..Default::default()
        });
        b.store.put(&alice(), &p).await.unwrap();
        for w in wrote {
            match w {
                Wrote::Artifact => {
                    b.graph
                        .add_items(
                            &alice(),
                            &p.change_id,
                            vec![ChangeItem {
                                r#type: domain::TYPE_ARTIFACT,
                                target: "doc".into(),
                                data: obj(json!({"seen": "ok"})),
                                produced_by: "fetch_doc".into(),
                                execution: EXEC.into(),
                                ..Default::default()
                            }],
                        )
                        .await
                        .unwrap();
                }
                Wrote::Record => {
                    journal::append(
                        &alice(),
                        &*b.graph,
                        &[journal::Record {
                            id: EXEC.into(),
                            change_id: p.change_id.clone(),
                            process_id: p.id.clone(),
                            kind: journal::KIND_ACTION.into(),
                            action: "fetch_doc".into(),
                            started_at: Some(Utc::now()),
                            ..Default::default()
                        }],
                    )
                    .await
                    .unwrap();
                }
            }
        }
        let got = b.run(&alice(), &p.id).await.unwrap();
        let n = calls.load(Ordering::SeqCst);
        assert_eq!(n, want_calls, "{name}: model calls");
        check(&got);
    }
}

/// WakeOnly delivers the signals of a replica to another, in process.
struct WakeOnly(Arc<Engine>);

#[async_trait]
impl Signals for WakeOnly {
    async fn wake(&self, _: &Ctx) -> goap_engine::Result<()> {
        self.0.woken();
        Ok(())
    }
    async fn interrupt(&self, _: &Ctx, id: &str, by: &str) -> goap_engine::Result<()> {
        self.0.interrupted(id, by);
        Ok(())
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_replica_at_capacity_leaves_the_run_to_the_pool() {
    let mut r = two_replicas(doc_client()).await;
    // a is full; b has room and is woken by a's signal
    r.a.max_runs = 1;
    r.b.max_runs = 1;
    let b = Arc::new(r.b);
    r.a.signals = Some(Arc::new(WakeOnly(b.clone())));
    let a = Arc::new(r.a);
    Engine::internals_of(&a).set_inflight(1);
    let (ctx, cancel) = ctx().with_cancel();
    {
        let b = b.clone();
        tokio::spawn(async move { b.recover_loop(&ctx, Duration::from_secs(3600)).await });
    }
    let p = start(&a, &r.base).await;
    Engine::internals_of(&a).schedule(&p.id);
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        let got = b.store.get(&alice(), &p.id).await.unwrap();
        if got.status == STATUS_COMPLETED {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "not run by the replica with room: {}",
            got.status
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    drop(cancel);
    b.drain().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_interrupt_signal_stops_the_run_at_once() {
    let called = Arc::new(Notify::new());
    let mut r = two_replicas(blocking_client(called.clone())).await;
    // renewals too rare to carry the ask in time
    r.a.lock_ttl = Duration::from_secs(60);
    r.b.lock_ttl = Duration::from_secs(60);
    let a = Arc::new(r.a);
    r.b.signals = Some(Arc::new(WakeOnly(a.clone())));
    let b = Arc::new(r.b);
    let p = start(&a, &r.base).await;
    let (tx, mut done) = mpsc::channel::<()>(1);
    {
        let (a, id) = (a.clone(), p.id.clone());
        tokio::spawn(async move {
            let _ = a.run(&alice(), &id).await;
            let _ = tx.send(()).await;
        });
    }
    called.notified().await;
    let began = Instant::now();
    let c = b.cancel(&alice(), &p.id, "").await.unwrap();
    done.recv().await;
    assert!(
        c.status == STATUS_CANCELLED && began.elapsed() <= Duration::from_secs(3),
        "cancelled {} after {:?}",
        c.status,
        began.elapsed()
    );
}
