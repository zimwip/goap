//! The cache of a typed view of the head of a namespace's main branch.

use std::fmt;
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, Link, MAIN_BRANCH, Node};
use goap_graphapi::{Error, Port};

/// Graph is the part of the graph a snapshot reads.
#[async_trait]
pub trait Graph: Send + Sync {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error>;
    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error>;
}

/// Every graph port reads a snapshot.
#[async_trait]
impl<T: Port + ?Sized> Graph for T {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline, Error> {
        Port::branch_head(self, ctx, namespace, name).await
    }
    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        Port::baseline_graph(self, ctx, id).await
    }
}

/// Build makes the view of a baseline; the graph without any baseline yields `build("", [], [])`.
pub type Build<T> = Arc<dyn Fn(&BaselineID, &[Node], &[Link]) -> T + Send + Sync>;

/// The error of a read of the graph: the last snapshot (none before the first build) keeps serving and comes with it.
pub struct ReadError<T> {
    /// The snapshot last built and its baseline, if any.
    pub last: Option<(T, BaselineID)>,
    pub error: Error,
}

impl<T> ReadError<T> {
    /// The snapshot last built, if any.
    pub fn last_view(&self) -> Option<&T> {
        self.last.as_ref().map(|(v, _)| v)
    }
}

impl<T> fmt::Debug for ReadError<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ReadError")
            .field("last", &self.last.as_ref().map(|(_, id)| id))
            .field("error", &self.error)
            .finish()
    }
}

impl<T> fmt::Display for ReadError<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(&self.error, f)
    }
}

impl<T> std::error::Error for ReadError<T> {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&self.error)
    }
}

struct State<T> {
    cur: Option<T>,
    id: BaselineID,
    checked: Option<Instant>,
}

/// Cache holds the snapshot of the head of `namespace`'s main. The head is looked at most once per TTL
/// (one second by default). When the graph cannot be read the last snapshot keeps serving and the error
/// is returned with it.
pub struct Cache<T> {
    pub graph: Arc<dyn Graph>,
    pub namespace: String,
    /// Zero: one second.
    pub ttl: Duration,
    pub build: Build<T>,

    // serializes the reads of the graph (held across them)
    reading: tokio::sync::Mutex<()>,
    state: parking_lot::Mutex<State<T>>,
}

impl<T: Clone + Send + Sync> Cache<T> {
    /// A cache of the head of `namespace`'s main, looked at once per second.
    pub fn new(
        graph: Arc<dyn Graph>,
        namespace: impl Into<String>,
        build: impl Fn(&BaselineID, &[Node], &[Link]) -> T + Send + Sync + 'static,
    ) -> Self {
        Self {
            graph,
            namespace: namespace.into(),
            ttl: Duration::ZERO,
            build: Arc::new(build),
            reading: tokio::sync::Mutex::new(()),
            state: parking_lot::Mutex::new(State {
                cur: None,
                id: BaselineID::default(),
                checked: None,
            }),
        }
    }

    /// The cache looking at the head once per `ttl`.
    pub fn with_ttl(mut self, ttl: Duration) -> Self {
        self.ttl = ttl;
        self
    }

    /// Get returns the current snapshot and the baseline it was built from.
    pub async fn get(&self, ctx: &Ctx) -> Result<(T, BaselineID), ReadError<T>> {
        self.read(ctx, false).await
    }

    /// Peek returns the snapshot last built, with no look at the graph: None before the first get.
    pub fn peek(&self) -> Option<T> {
        self.state.lock().cur.clone()
    }

    /// Fresh is get without the TTL: the head is looked at now (before a write that builds on what it reads).
    pub async fn fresh(&self, ctx: &Ctx) -> Result<(T, BaselineID), ReadError<T>> {
        self.read(ctx, true).await
    }

    fn current(&self) -> Option<(T, BaselineID)> {
        let st = self.state.lock();
        st.cur.clone().map(|c| (c, st.id.clone()))
    }

    fn stale(&self, error: Error) -> ReadError<T> {
        ReadError {
            last: self.current(),
            error,
        }
    }

    fn set(&self, cur: T, id: BaselineID, checked: bool) -> (T, BaselineID) {
        let mut st = self.state.lock();
        st.cur = Some(cur.clone());
        st.id = id.clone();
        if checked {
            st.checked = Some(Instant::now());
        }
        (cur, id)
    }

    async fn read(&self, ctx: &Ctx, fresh: bool) -> Result<(T, BaselineID), ReadError<T>> {
        let _reading = self.reading.lock().await;
        let ttl = if self.ttl.is_zero() {
            Duration::from_secs(1)
        } else {
            self.ttl
        };
        {
            let st = self.state.lock();
            if !fresh
                && let (Some(cur), Some(checked)) = (&st.cur, st.checked)
                && checked.elapsed() < ttl
            {
                return Ok((cur.clone(), st.id.clone()));
            }
        }
        let head = match self
            .graph
            .branch_head(ctx, &self.namespace, MAIN_BRANCH)
            .await
        {
            Ok(h) => h,
            Err(e) if e.is_not_found() => {
                let empty = BaselineID::default();
                let cur = (self.build)(&empty, &[], &[]);
                return Ok(self.set(cur, empty, true));
            }
            Err(e) => return Err(self.stale(e)),
        };
        {
            let mut st = self.state.lock();
            st.checked = Some(Instant::now());
            if let Some(cur) = &st.cur
                && st.id == head.id
            {
                return Ok((cur.clone(), st.id.clone()));
            }
        }
        let (nodes, links) = match self.graph.baseline_graph(ctx, &head.id).await {
            Ok(g) => g,
            Err(e) => return Err(self.stale(e)),
        };
        let cur = (self.build)(&head.id, &nodes, &links);
        Ok(self.set(cur, head.id, false))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct Fake {
        head: parking_lot::Mutex<Option<String>>,
        fail: parking_lot::Mutex<bool>,
        reads: AtomicUsize,
    }

    #[async_trait]
    impl Graph for Fake {
        async fn branch_head(&self, _: &Ctx, _: &str, _: &str) -> Result<Baseline, Error> {
            if *self.fail.lock() {
                return Err(Error::msg("down"));
            }
            match self.head.lock().clone() {
                Some(id) => Ok(Baseline {
                    id: id.into(),
                    ..Default::default()
                }),
                None => Err(Error::not_found("no head")),
            }
        }
        async fn baseline_graph(
            &self,
            _: &Ctx,
            _: &BaselineID,
        ) -> Result<(Vec<Node>, Vec<Link>), Error> {
            self.reads.fetch_add(1, Ordering::SeqCst);
            Ok((vec![Node::default()], vec![]))
        }
    }

    #[tokio::test]
    async fn test_cache() {
        let g = Arc::new(Fake {
            head: parking_lot::Mutex::new(None),
            fail: parking_lot::Mutex::new(false),
            reads: AtomicUsize::new(0),
        });
        let c = Cache::new(
            g.clone(),
            "platform",
            |id: &BaselineID, n: &[Node], _: &[Link]| format!("{id}:{}", n.len()),
        );
        let ctx = Ctx::background();
        assert_eq!(c.peek(), None);
        // no baseline: build of the empty graph
        let (v, id) = c.get(&ctx).await.unwrap();
        assert_eq!((v.as_str(), id.as_str()), (":0", ""));
        // within the TTL the head is not looked at again; fresh looks
        *g.head.lock() = Some("B1".into());
        assert_eq!(c.get(&ctx).await.unwrap().0, ":0");
        assert_eq!(c.fresh(&ctx).await.unwrap().0, "B1:1");
        assert_eq!(c.fresh(&ctx).await.unwrap().0, "B1:1");
        assert_eq!(g.reads.load(Ordering::SeqCst), 1, "same head, no rebuild");
        // unreadable: the last snapshot keeps serving with the error
        *g.fail.lock() = true;
        let err = c.fresh(&ctx).await.unwrap_err();
        assert_eq!(err.last_view().map(String::as_str), Some("B1:1"));
        assert_eq!(err.to_string(), "down");
        assert_eq!(c.peek().as_deref(), Some("B1:1"));
    }
}
