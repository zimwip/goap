//! The errors of a call and their telemetry type, and the observer of the retries of a provider call.

use std::{error::Error as StdError, sync::Arc, time::Duration};

use goap_ctx::{Ctx, CtxError};

/// The errors of the completion contract.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrRefusal is the error of a call the model declined.
    #[error("model refused the request")]
    Refusal,
    /// The context of the call is done (`context.Canceled` / `context.DeadlineExceeded`).
    #[error(transparent)]
    Ctx(#[from] CtxError),
    /// An error that states its own telemetry type (Go's `ErrorType() string` method): the HTTP status of a provider
    /// answer, ...
    #[error("{message}")]
    Typed { error_type: String, message: String },
    /// An error wrapped with a message (Go's `fmt.Errorf("msg: %w", err)`): "msg: err".
    #[error("{message}: {source}")]
    Wrapped {
        message: String,
        #[source]
        source: Box<Error>,
    },
    /// DecodeJSON found no JSON in the answer.
    #[error("no JSON in model answer")]
    NoJson,
    /// DecodeJSON could not decode the JSON of the answer.
    #[error("decode model answer: {0}")]
    Decode(#[source] serde_json::Error),
    /// Any other error, as its message.
    #[error("{0}")]
    Message(String),
    /// Any other error.
    #[error(transparent)]
    Other(Box<dyn StdError + Send + Sync>),
}

impl Error {
    /// An error carrying its own telemetry type.
    pub fn typed(error_type: impl Into<String>, message: impl Into<String>) -> Error {
        Error::Typed {
            error_type: error_type.into(),
            message: message.into(),
        }
    }

    /// The error wrapped with a message: `fmt.Errorf("<message>: %w", err)`.
    pub fn wrap(self, message: impl Into<String>) -> Error {
        Error::Wrapped {
            message: message.into(),
            source: Box::new(self),
        }
    }

    /// The error behind the wrappings.
    pub fn root(&self) -> &Error {
        match self {
            Error::Wrapped { source, .. } => source.root(),
            e => e,
        }
    }

    /// `errors.Is(err, ErrRefusal)`.
    pub fn is_refusal(&self) -> bool {
        matches!(self.root(), Error::Refusal)
    }

    /// `errors.Is(err, context.DeadlineExceeded)`.
    pub fn is_deadline_exceeded(&self) -> bool {
        matches!(self.root(), Error::Ctx(CtxError::DeadlineExceeded))
    }

    /// `errors.Is(err, context.Canceled)`.
    pub fn is_canceled(&self) -> bool {
        matches!(self.root(), Error::Ctx(CtxError::Canceled))
    }

    /// The telemetry type of the error (see [`error_type`]).
    pub fn error_type(&self) -> String {
        error_type(Some(self))
    }
}

/// ErrorType classifies the error of a call for telemetry (the OpenTelemetry error.type): the HTTP status of a
/// provider answer, "timeout", "canceled", "refusal", else "_OTHER". An error states its own type as an
/// [`Error::Typed`] anywhere in its chain; an error of another type is looked through its sources.
pub fn error_type(err: Option<&(dyn StdError + 'static)>) -> String {
    let Some(err) = err else {
        return String::new();
    };
    let chain = || std::iter::successors(Some(err), |e: &&(dyn StdError + 'static)| (*e).source());
    if let Some(t) = chain().find_map(|e| match as_llm(e) {
        Some(Error::Typed { error_type, .. }) => Some(error_type.clone()),
        _ => None,
    }) {
        return t;
    }
    if chain().any(|e| matches!(as_llm(e), Some(Error::Refusal))) {
        return "refusal".into();
    }
    let ctx_err = |want: CtxError| {
        chain().any(|e| {
            e.downcast_ref::<CtxError>() == Some(&want)
                || matches!(as_llm(e), Some(Error::Ctx(c)) if *c == want)
        })
    };
    if ctx_err(CtxError::DeadlineExceeded) {
        return "timeout".into();
    }
    if ctx_err(CtxError::Canceled) {
        return "canceled".into();
    }
    "_OTHER".into()
}

/// The error as an [`Error`] of this package (a wrapped one is a `Box<Error>` in the chain of sources).
fn as_llm<'a>(e: &'a (dyn StdError + 'static)) -> Option<&'a Error> {
    e.downcast_ref::<Error>()
        .or_else(|| e.downcast_ref::<Box<Error>>().map(|b| &**b))
}

/// RetryObserver is told of every retry of a provider call: the attempt that failed (1-based), its error and the
/// wait before the next one.
pub type RetryObserver = Arc<dyn Fn(u32, &Error, Duration) + Send + Sync>;

#[derive(Clone)]
struct RetryKey(RetryObserver);

/// WithRetryObserver returns a context whose provider calls report their retries to o (the instrumentation of the
/// gateway installs one per call).
pub fn with_retry_observer(ctx: &Ctx, o: RetryObserver) -> Ctx {
    ctx.with(RetryKey(o))
}

/// RetryObserverFrom returns the observer of ctx, or one that does nothing.
pub fn retry_observer_from(ctx: &Ctx) -> RetryObserver {
    match ctx.value::<RetryKey>() {
        Some(k) => k.0.clone(),
        None => Arc::new(|_, _, _| {}),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU32, Ordering};

    #[test]
    fn error_types() {
        assert_eq!(error_type(None), "");
        assert_eq!(Error::Refusal.error_type(), "refusal");
        assert_eq!(Error::Refusal.wrap("anthropic").error_type(), "refusal");
        assert_eq!(
            Error::Refusal.wrap("anthropic").to_string(),
            "anthropic: model refused the request"
        );
        assert_eq!(
            Error::typed("429", "too many").wrap("x").error_type(),
            "429"
        );
        assert_eq!(
            Error::from(CtxError::DeadlineExceeded).error_type(),
            "timeout"
        );
        assert_eq!(
            Error::from(CtxError::Canceled).wrap("call").error_type(),
            "canceled"
        );
        assert_eq!(error_type(Some(&CtxError::Canceled)), "canceled");
        assert_eq!(Error::Message("boom".into()).error_type(), "_OTHER");
        assert!(Error::Refusal.wrap("a").wrap("b").is_refusal());
    }

    #[test]
    fn retry_observer() {
        let ctx = Ctx::background();
        retry_observer_from(&ctx)(1, &Error::Refusal, Duration::ZERO);
        let n = Arc::new(AtomicU32::new(0));
        let seen = n.clone();
        let ctx = with_retry_observer(
            &ctx,
            Arc::new(move |attempt, _, _| {
                seen.store(attempt, Ordering::SeqCst);
            }),
        );
        retry_observer_from(&ctx)(2, &Error::Refusal, Duration::from_millis(1));
        assert_eq!(n.load(Ordering::SeqCst), 2);
    }
}
