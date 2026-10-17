//! The request context, Go's `context.Context` made an explicit value (docs/rust-port.md).
//!
//! A [`Ctx`] is immutable and cheap to clone: [`Ctx::with`] returns a child carrying one more value (the principal of
//! `authz`, the call metadata of `llm`, ...), [`Ctx::with_cancel`] / [`Ctx::with_timeout`] a child that can be
//! cancelled. A value is looked up by its type, nearest first, the way `ctx.Value(key)` walks the parents; each
//! package keeps its own `with_x(ctx, x)` / `x_from(ctx)` pair over a private key type, as it did in Go.
use std::{
    any::{Any, TypeId},
    fmt,
    sync::Arc,
    time::{Duration, Instant},
};

use tokio_util::sync::CancellationToken;

/// Why a context is done: `context.Canceled` / `context.DeadlineExceeded`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CtxError {
    Canceled,
    DeadlineExceeded,
}

impl fmt::Display for CtxError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            CtxError::Canceled => f.write_str("context canceled"),
            CtxError::DeadlineExceeded => f.write_str("context deadline exceeded"),
        }
    }
}

impl std::error::Error for CtxError {}

struct Inner {
    parent: Option<Ctx>,
    value: Option<(TypeId, Arc<dyn Any + Send + Sync>)>,
    token: CancellationToken,
    deadline: Option<Instant>,
}

/// A request context. `Ctx::default()` is `context.Background()`.
#[derive(Clone)]
pub struct Ctx(Arc<Inner>);

impl Default for Ctx {
    fn default() -> Self {
        Self::background()
    }
}

impl fmt::Debug for Ctx {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Ctx").field("done", &self.is_done()).field("deadline", &self.0.deadline).finish()
    }
}

/// Cancels the context [`Ctx::with_cancel`] returned with it (and its children); dropping it cancels nothing.
#[derive(Clone)]
pub struct CancelHandle(CancellationToken);

impl CancelHandle {
    pub fn cancel(&self) {
        self.0.cancel();
    }
}

impl Ctx {
    /// The root context: no value, never cancelled.
    pub fn background() -> Self {
        Ctx(Arc::new(Inner { parent: None, value: None, token: CancellationToken::new(), deadline: None }))
    }

    fn child(&self, value: Option<(TypeId, Arc<dyn Any + Send + Sync>)>, token: CancellationToken, deadline: Option<Instant>) -> Self {
        Ctx(Arc::new(Inner { parent: Some(self.clone()), value, token, deadline }))
    }

    /// A child carrying `v`, found by [`Ctx::value`] under its type.
    pub fn with<T: Any + Send + Sync>(&self, v: T) -> Self {
        self.child(Some((TypeId::of::<T>(), Arc::new(v))), self.0.token.clone(), self.0.deadline)
    }

    /// The nearest value of type `T`.
    pub fn value<T: Any + Send + Sync>(&self) -> Option<&T> {
        let mut c = Some(self);
        while let Some(ctx) = c {
            if let Some((id, v)) = &ctx.0.value
                && *id == TypeId::of::<T>()
            {
                return v.downcast_ref::<T>();
            }
            c = ctx.0.parent.as_ref();
        }
        None
    }

    /// A child that `cancel` cancels, as the parent's cancellation does.
    pub fn with_cancel(&self) -> (Self, CancelHandle) {
        let t = self.0.token.child_token();
        (self.child(None, t.clone(), self.0.deadline), CancelHandle(t))
    }

    /// A child done after `d` (or when the parent is).
    pub fn with_timeout(&self, d: Duration) -> (Self, CancelHandle) {
        self.with_deadline(Instant::now() + d)
    }

    /// A child done at `at` (the earlier of `at` and the parent's deadline).
    pub fn with_deadline(&self, at: Instant) -> (Self, CancelHandle) {
        let at = self.0.deadline.map_or(at, |d| d.min(at));
        let t = self.0.token.child_token();
        (self.child(None, t.clone(), Some(at)), CancelHandle(t))
    }

    /// A child that keeps the values but is never cancelled: `context.WithoutCancel`.
    pub fn without_cancel(&self) -> Self {
        self.child(None, CancellationToken::new(), None)
    }

    pub fn deadline(&self) -> Option<Instant> {
        self.0.deadline
    }

    /// `ctx.Err()`: why the context is done, `None` while it is not.
    pub fn err(&self) -> Option<CtxError> {
        if self.0.token.is_cancelled() {
            Some(CtxError::Canceled)
        } else if self.0.deadline.is_some_and(|d| Instant::now() >= d) {
            Some(CtxError::DeadlineExceeded)
        } else {
            None
        }
    }

    pub fn is_done(&self) -> bool {
        self.err().is_some()
    }

    /// `<-ctx.Done()`: resolves when the context is cancelled or its deadline passes.
    pub async fn done(&self) -> CtxError {
        match self.0.deadline {
            Some(d) => tokio::select! {
                _ = self.0.token.cancelled() => CtxError::Canceled,
                _ = tokio::time::sleep_until(d.into()) => CtxError::DeadlineExceeded,
            },
            None => {
                self.0.token.cancelled().await;
                CtxError::Canceled
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Debug, PartialEq)]
    struct Who(&'static str);

    #[test]
    fn values_nearest_first() {
        let a = Ctx::background().with(Who("a"));
        let b = a.with(Who("b")).with(3u8);
        assert_eq!(b.value::<Who>(), Some(&Who("b")));
        assert_eq!(a.value::<Who>(), Some(&Who("a")));
        assert_eq!(b.value::<u8>(), Some(&3));
        assert!(a.value::<u8>().is_none());
    }

    #[test]
    fn cancel_reaches_children_not_parents() {
        let (c, cancel) = Ctx::background().with_cancel();
        let child = c.with(1u32);
        assert!(child.err().is_none());
        cancel.cancel();
        assert_eq!(child.err(), Some(CtxError::Canceled));
        assert!(child.without_cancel().err().is_none());
    }

    #[tokio::test]
    async fn deadline() {
        let (c, _h) = Ctx::background().with_timeout(Duration::from_millis(5));
        assert_eq!(c.done().await, CtxError::DeadlineExceeded);
        assert_eq!(c.err(), Some(CtxError::DeadlineExceeded));
    }
}
