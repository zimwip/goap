//! Interceptors: connect-go's `connect.Interceptor`, on handlers and on clients.
use std::{fmt, future::Future, pin::Pin, sync::Arc};

use async_trait::async_trait;
use goap_ctx::Ctx;

use crate::{
    error::ConnectError,
    request::{AnyRequest, AnyResponse},
};

/// A boxed future, as the chain passes them.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// The end of a unary chain: the handler (server side) or the HTTP call (client side).
pub type UnaryEnd = dyn Fn(Ctx, AnyRequest) -> BoxFuture<'static, Result<AnyResponse, ConnectError>> + Send + Sync;

/// Wraps the calls of a handler or a client.
///
/// [`Interceptor::unary`] is connect-go's `UnaryInterceptorFunc`: it gets the context, the request (its
/// [`crate::Spec`], headers and erased message) and the rest of the chain; it may change the context (put the
/// caller's principal in it), the headers (a client adding its credentials), refuse the call (return an error
/// without calling `next`) or look at the response. As in connect-go the unary hook does not see streams; a stream
/// goes through [`Interceptor::stream`] when it opens (before its handler runs, or before the client sends it), which
/// may change the context and the headers or refuse it. Both default to passing the call through.
///
/// The first interceptor of a list is the outermost: it runs first on the way in and last on the way out.
///
/// ```
/// use goap_connect::{async_trait, AnyRequest, AnyResponse, ConnectError, Ctx, Interceptor, Next};
///
/// struct RequireToken;
///
/// #[async_trait]
/// impl Interceptor for RequireToken {
///     async fn unary(&self, ctx: Ctx, req: AnyRequest, next: Next<'_>) -> Result<AnyResponse, ConnectError> {
///         if !req.spec.is_client && req.header("x-token").is_empty() {
///             return Err(ConnectError::unauthenticated("no token"));
///         }
///         next.run(ctx, req).await
///     }
/// }
/// ```
#[async_trait]
pub trait Interceptor: Send + Sync + 'static {
    async fn unary(&self, ctx: Ctx, req: AnyRequest, next: Next<'_>) -> Result<AnyResponse, ConnectError> {
        next.run(ctx, req).await
    }

    /// The opening of a server stream: returns the context the stream runs with.
    async fn stream(&self, ctx: Ctx, req: &mut AnyRequest) -> Result<Ctx, ConnectError> {
        let _ = req;
        Ok(ctx)
    }
}

/// The rest of a unary chain, given to [`Interceptor::unary`].
pub struct Next<'a> {
    rest: &'a [Arc<dyn Interceptor>],
    end: &'a UnaryEnd,
}

impl<'a> Next<'a> {
    pub(crate) fn new(rest: &'a [Arc<dyn Interceptor>], end: &'a UnaryEnd) -> Self {
        Next { rest, end }
    }

    /// Calls the rest of the chain.
    pub async fn run(self, ctx: Ctx, req: AnyRequest) -> Result<AnyResponse, ConnectError> {
        match self.rest.split_first() {
            Some((first, rest)) => first.unary(ctx, req, Next { rest, end: self.end }).await,
            None => (self.end)(ctx, req).await,
        }
    }
}

/// An ordered list of interceptors, cheap to clone (the first is the outermost).
#[derive(Clone, Default)]
pub struct Interceptors(Arc<[Arc<dyn Interceptor>]>);

impl Interceptors {
    /// No interceptor.
    pub fn none() -> Self {
        Self::default()
    }

    /// These interceptors, the first outermost.
    pub fn new(list: Vec<Arc<dyn Interceptor>>) -> Self {
        Interceptors(list.into())
    }

    /// The list with `i` added innermost.
    pub fn with(&self, i: impl Interceptor) -> Self {
        self.with_arc(Arc::new(i))
    }

    pub fn with_arc(&self, i: Arc<dyn Interceptor>) -> Self {
        let mut v: Vec<_> = self.0.iter().cloned().collect();
        v.push(i);
        Interceptors(v.into())
    }

    /// `self` then `other` (`other` innermost).
    pub fn chain(&self, other: &Interceptors) -> Self {
        Interceptors(self.0.iter().chain(other.0.iter()).cloned().collect())
    }

    pub fn as_slice(&self) -> &[Arc<dyn Interceptor>] {
        &self.0
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    pub fn len(&self) -> usize {
        self.0.len()
    }

    /// Runs a unary call through the chain to `end`.
    pub async fn run_unary(&self, ctx: Ctx, req: AnyRequest, end: &UnaryEnd) -> Result<AnyResponse, ConnectError> {
        Next::new(&self.0, end).run(ctx, req).await
    }

    /// Runs the opening of a stream through every [`Interceptor::stream`], outermost first.
    pub async fn run_stream(&self, mut ctx: Ctx, req: &mut AnyRequest) -> Result<Ctx, ConnectError> {
        for i in self.0.iter() {
            ctx = i.stream(ctx, req).await?;
        }
        Ok(ctx)
    }
}

impl fmt::Debug for Interceptors {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "Interceptors({})", self.0.len())
    }
}

impl From<Vec<Arc<dyn Interceptor>>> for Interceptors {
    fn from(v: Vec<Arc<dyn Interceptor>>) -> Self {
        Interceptors::new(v)
    }
}

/// The interceptor of a synchronous function run before the call, unary or stream: it may rewrite the context and
/// the request (headers) or refuse the call. The most common shape (put the principal of the headers in the
/// context, stamp a command, add a credential to a client's requests).
///
/// ```
/// use goap_connect::{Before, ConnectError, Ctx, Interceptors};
///
/// #[derive(Clone)]
/// struct Caller(String);
///
/// let identify = Before(|ctx: Ctx, req: &mut goap_connect::AnyRequest| {
///     let who = req.header("x-goap-subject").to_string();
///     if who.is_empty() {
///         return Err(ConnectError::unauthenticated("the caller is not identified"));
///     }
///     Ok(ctx.with(Caller(who)))
/// });
/// let _chain = Interceptors::none().with(identify);
/// ```
pub struct Before<F>(pub F);

#[async_trait]
impl<F> Interceptor for Before<F>
where
    F: Fn(Ctx, &mut AnyRequest) -> Result<Ctx, ConnectError> + Send + Sync + 'static,
{
    async fn unary(&self, ctx: Ctx, mut req: AnyRequest, next: Next<'_>) -> Result<AnyResponse, ConnectError> {
        let ctx = (self.0)(ctx, &mut req)?;
        next.run(ctx, req).await
    }

    async fn stream(&self, ctx: Ctx, req: &mut AnyRequest) -> Result<Ctx, ConnectError> {
        (self.0)(ctx, req)
    }
}
