//! Locks and replicas (ADR 0124, Go's `locks.go`): the work on a key is serialized in this replica by a mutex and
//! across the replicas by a shared lease (Locker).

use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use tokio::sync::OwnedMutexGuard;

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::process::STATUS_RUNNING;

/// Locker is the lock service engine replicas share (ADR 0124): a lock is a lease on a key, held by one replica,
/// renewed while it works and taken over by another once it expires.
#[async_trait]
pub trait Locker: Send + Sync {
    /// acquire takes the lock of key for ttl, or renews it when this replica holds it already. `held` is false while
    /// another replica's lease runs; `interrupt` is what another replica asked of the holder, "" for nothing.
    async fn acquire(&self, ctx: &Ctx, key: &str, ttl: Duration) -> Result<(bool, String)>;
    /// release lets the lock of key go when this replica holds it.
    async fn release(&self, ctx: &Ctx, key: &str) -> Result<()>;
    /// interrupt asks the replica holding the lock of key to let go of it (by: who asked); false when no other
    /// replica holds it.
    async fn interrupt(&self, ctx: &Ctx, key: &str, by: &str) -> Result<bool>;
}

/// OrphanFinder is implemented by a store that knows which running processes no replica works on: the processes
/// running, written before the instant given, whose lock nobody holds.
#[async_trait]
pub trait OrphanFinder: Send + Sync {
    async fn orphans(&self, ctx: &Ctx, before: DateTime<Utc>) -> Result<Vec<String>>;
}

/// Signals carries hints between the replicas (ADR 0124): best effort. The receiving side calls Engine::woken and
/// Engine::interrupted.
#[async_trait]
pub trait Signals: Send + Sync {
    /// wake tells the replicas that a process is runnable and no replica runs it.
    async fn wake(&self, ctx: &Ctx) -> Result<()>;
    /// interrupt tells the holder of the lock of id that another replica asked it to let go.
    async fn interrupt(&self, ctx: &Ctx, id: &str, by: &str) -> Result<()>;
}

/// The lease of a lock when Engine::lock_ttl is not set.
pub const DEFAULT_LOCK_TTL: Duration = Duration::from_secs(30);

/// A shared lock this replica holds, and whether another replica asked it to let go.
pub(crate) struct Held {
    key: String,
    interrupted: parking_lot::Mutex<bool>,
}

impl Held {
    fn interrupt(&self, e: &Engine, by: &str) {
        if by.is_empty() {
            return;
        }
        let mut i = self.interrupted.lock();
        if *i {
            return;
        }
        *i = true;
        e.cancelling.lock().insert(self.key.clone(), by.to_string());
        e.cancel_run(&self.key);
    }

    fn done(&self) -> bool {
        *self.interrupted.lock()
    }
}

/// The unlock of Engine::lock: dropping it lets the key go (the shared lock released first, in the background).
pub struct Unlock {
    guard: Option<OwnedMutexGuard<()>>,
    shared: Option<Shared>,
}

struct Shared {
    e: Arc<Engine>,
    key: String,
    held: Arc<Held>,
    stop: tokio::sync::watch::Sender<bool>,
    renew: tokio::task::JoinHandle<()>,
}

impl Drop for Unlock {
    fn drop(&mut self) {
        let guard = self.guard.take();
        let Some(s) = self.shared.take() else {
            drop(guard);
            return;
        };
        let _ = s.stop.send(true);
        let Ok(rt) = tokio::runtime::Handle::try_current() else {
            s.renew.abort();
            drop(guard);
            return;
        };
        rt.spawn(async move {
            let _ = s.renew.await;
            {
                let mut held = s.e.held.lock();
                if held.get(&s.key).is_some_and(|h| Arc::ptr_eq(h, &s.held)) {
                    held.remove(&s.key);
                }
            }
            if s.held.done() {
                s.e.cancelling.lock().remove(&s.key);
            }
            if let Some(l) = &s.e.locks
                && let Err(err) = l.release(&Ctx::background(), &s.key).await
            {
                tracing::warn!(key = %s.key, err = %err, "lock release");
            }
            drop(guard);
        });
    }
}

impl Engine {
    pub(crate) fn lock_ttl(&self) -> Duration {
        if self.lock_ttl > Duration::ZERO {
            self.lock_ttl
        } else {
            DEFAULT_LOCK_TTL
        }
    }

    /// lock serializes the work on a key (a process id, or a named section): in this replica by a mutex, across the
    /// replicas by the shared lock when Engine::locks is set. Dropping the returned guard unlocks.
    pub(crate) async fn lock(self: &Arc<Self>, id: &str) -> Unlock {
        let mu = self
            .key_locks
            .lock()
            .entry(id.to_string())
            .or_insert_with(|| Arc::new(tokio::sync::Mutex::new(())))
            .clone();
        let guard = mu.lock_owned().await;
        if self.locks.is_none() {
            return Unlock {
                guard: Some(guard),
                shared: None,
            };
        }
        let shared = self.hold(id).await;
        Unlock {
            guard: Some(guard),
            shared: Some(shared),
        }
    }

    /// hold takes the shared lock of key, waiting while another replica holds it, and keeps it renewed every tenth of
    /// its lease until released.
    async fn hold(self: &Arc<Self>, key: &str) -> Shared {
        let locks = self.locks.clone().expect("hold without Locks");
        let ctx = Ctx::background();
        let ttl = self.lock_ttl();
        let mut wait = Duration::from_millis(20);
        let interrupt;
        loop {
            match locks.acquire(&ctx, key, ttl).await {
                Err(err) => tracing::warn!(key, err = %err, "lock"),
                Ok((true, by)) => {
                    interrupt = by;
                    break;
                }
                Ok(_) => {}
            }
            tokio::time::sleep(wait).await;
            wait = (wait * 2).min(Duration::from_secs(1));
        }
        let h = Arc::new(Held {
            key: key.to_string(),
            interrupted: parking_lot::Mutex::new(false),
        });
        self.held.lock().insert(key.to_string(), h.clone());
        h.interrupt(self, &interrupt);
        let (stop, mut stopped) = tokio::sync::watch::channel(false);
        let e = self.clone();
        let k = key.to_string();
        let hh = h.clone();
        let renew = tokio::spawn(async move {
            let period = (ttl / 10).max(Duration::from_millis(10));
            let mut tick = tokio::time::interval_at(tokio::time::Instant::now() + period, period);
            loop {
                tokio::select! {
                    _ = stopped.changed() => return,
                    _ = tick.tick() => {
                        match locks.acquire(&ctx, &k, ttl).await {
                            Err(err) => tracing::warn!(key = %k, err = %err, "lock renewal"),
                            Ok((false, _)) => {
                                tracing::error!(key = %k, "lock lost: another replica took over");
                                e.cancel_run(&k);
                                return;
                            }
                            Ok((true, by)) => hh.interrupt(&e, &by),
                        }
                    }
                }
            }
        });
        Shared {
            e: self.clone(),
            key: key.to_string(),
            held: h,
            stop,
            renew,
        }
    }

    /// cancel_run cancels the run in flight on id in this replica, if any.
    pub(crate) fn cancel_run(&self, id: &str) {
        if let Some(h) = self.running.lock().get(id) {
            h.cancel.cancel();
        }
    }

    /// interrupt asks the run in flight on id to let go, wherever it runs.
    pub(crate) async fn interrupt(&self, ctx: &Ctx, id: &str, by: &str) {
        self.cancel_run(id);
        let Some(locks) = &self.locks else {
            return;
        };
        match locks.interrupt(ctx, id, by).await {
            Err(err) => tracing::warn!(process = id, err = %err, "interrupt"),
            Ok(asked) => {
                if asked
                    && let Some(s) = &self.signals
                    && let Err(err) = s.interrupt(ctx, id, by).await
                {
                    tracing::warn!(process = id, err = %err, "interrupt signal");
                }
            }
        }
    }

    /// interrupted handles the signal of another replica asking the holder of the lock of id to let go.
    pub fn interrupted(&self, id: &str, by: &str) {
        let h = self.held.lock().get(id).cloned();
        if let Some(h) = h {
            h.interrupt(self, by);
        }
    }

    /// woken handles the signal of a replica that left a runnable process to the pool: Recover looks for it at once.
    pub fn woken(&self) {
        self.wake.notify_one();
    }

    /// at_capacity reports whether this replica runs as many processes as it may (Engine::max_runs).
    pub(crate) fn at_capacity(&self) -> bool {
        self.max_runs > 0 && self.inflight.load(Ordering::SeqCst) >= self.max_runs
    }

    /// leave leaves a runnable process to the pool (this replica is at capacity).
    pub(crate) fn leave(self: &Arc<Self>, id: &str) {
        tracing::info!(
            process = id,
            max_runs = self.max_runs,
            "process left to the pool: this replica is at capacity"
        );
        if let Some(s) = self.signals.clone() {
            self.background(async move {
                if let Err(err) = s.wake(&Ctx::background()).await {
                    tracing::warn!(err = %err, "wake signal");
                }
            });
        }
    }

    /// run_counted runs a process as one of the runs this replica holds (Engine::max_runs).
    pub(crate) async fn run_counted(self: &Arc<Self>, id: &str) {
        self.inflight.fetch_add(1, Ordering::SeqCst);
        if let Err(err) = self.run(&Ctx::background(), id).await {
            tracing::error!(process = id, err = %err, "run");
        }
        self.inflight.fetch_add(-1, Ordering::SeqCst);
        if self.locks.is_some() && self.max_runs > 0 {
            self.woken();
        }
    }

    /// recover_loop resumes the processes no replica works on (Go's `Recover`): at once for the processes written
    /// before the call, then every interval for those idle for a lease, and at once when woken. It returns when ctx
    /// is done; a store that cannot tell the orphans makes it return at once.
    pub async fn recover_loop(self: &Arc<Self>, ctx: &Ctx, every: Duration) {
        if self.store.orphan_finder().is_none() {
            return;
        }
        let every = if every.is_zero() {
            self.lock_ttl()
        } else {
            every
        };
        let lease = chrono::Duration::from_std(self.lock_ttl()).unwrap_or_default();
        let mut before = self.clock();
        loop {
            self.recover(ctx, before).await;
            tokio::select! {
                _ = ctx.done() => return,
                _ = tokio::time::sleep(every) => before = self.clock() - lease,
                _ = self.wake.notified() => before = self.clock() + chrono::Duration::milliseconds(1),
            }
        }
    }

    /// recover resumes the orphans written before an instant.
    async fn recover(self: &Arc<Self>, ctx: &Ctx, before: DateTime<Utc>) {
        let Some(finder) = self.store.orphan_finder() else {
            return;
        };
        let ids = match finder.orphans(ctx, before).await {
            Ok(ids) => ids,
            Err(err) => {
                tracing::warn!(err = %err, "recover");
                return;
            }
        };
        for id in ids {
            if self.at_capacity() {
                return;
            }
            if !self.recovering.lock().insert(id.clone()) {
                continue;
            }
            let p = match self.store.get(ctx, &id).await {
                Ok(p) if p.status == STATUS_RUNNING => p,
                _ => {
                    self.recovering.lock().remove(&id);
                    continue;
                }
            };
            if !p.parent_id.is_empty() && !self.known_by_parent(ctx, &p).await {
                self.recovering.lock().remove(&id);
                self.fail_orphan(ctx, &id).await;
                continue;
            }
            tracing::info!(process = %id, "resuming a process no replica runs");
            self.inflight.fetch_add(1, Ordering::SeqCst);
            let e = self.clone();
            self.background(async move {
                e.inflight.fetch_add(-1, Ordering::SeqCst);
                e.run_counted(&id).await;
                e.recovering.lock().remove(&id);
            });
        }
    }

    async fn known_by_parent(&self, ctx: &Ctx, p: &crate::process::Process) -> bool {
        let Ok(parent) = self.store.get(ctx, &p.parent_id).await else {
            return false;
        };
        if parent
            .pending
            .as_ref()
            .is_some_and(|t| t.child_process_id == p.id)
        {
            return true;
        }
        parent.children.values().any(|c| *c == p.id)
    }

    async fn fail_orphan(self: &Arc<Self>, ctx: &Ctx, id: &str) {
        let _u = self.lock(id).await;
        let Ok(mut p) = self.store.get(ctx, id).await else {
            return;
        };
        if p.status != STATUS_RUNNING {
            return;
        }
        self.fail(
            &mut p,
            &Error::msg("interrupted: the engine running it stopped before its parent recorded it"),
        );
        let ev = p.status.to_string();
        if let Err(err) = self.save(ctx, &mut p, &ev).await {
            tracing::warn!(process = id, err = %err, "recover");
        }
    }
}
