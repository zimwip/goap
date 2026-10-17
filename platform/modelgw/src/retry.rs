//! Retries of a provider call: a transient failure (a timeout, a rate limit, an overloaded or failing server, a
//! connection dropped) is tried again with an exponential backoff and jitter, honouring the provider's Retry-After.
//! The Anthropic provider retries on its own (`Anthropic`): its errors are not `StatusError`s and are never retried
//! here.

use std::collections::hash_map::RandomState;
use std::fmt;
use std::hash::{BuildHasher, Hasher};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use goap_ctx::Ctx;
use goap_llm as llm;

use crate::router::{Provider, Router};

pub const DEFAULT_RETRIES: i64 = 3;
pub(crate) const RETRY_BASE: Duration = Duration::from_secs(1);
pub(crate) const RETRY_CAP: Duration = Duration::from_secs(30);
pub(crate) const RETRY_AFTER_CAP: Duration = Duration::from_secs(60);

/// StatusError is a provider answer with an HTTP error status.
#[derive(Debug)]
pub struct StatusError {
    pub code: u16,
    /// From the Retry-After header, zero when absent.
    pub retry_after: Duration,
    pub msg: String,
    // the classification of llm::error_type (Go's ErrorType method): the HTTP status, found in the chain of sources
    typed: llm::Error,
}

impl StatusError {
    pub fn new(code: u16, msg: impl Into<String>) -> Self {
        let msg = msg.into();
        StatusError {
            code,
            retry_after: Duration::ZERO,
            typed: llm::Error::typed(code.to_string(), msg.clone()),
            msg,
        }
    }

    /// The error as an error of the completion contract.
    pub fn into_llm(self) -> llm::Error {
        llm::Error::Other(Box::new(self))
    }

    /// ErrorType: the HTTP status.
    pub fn error_type(&self) -> String {
        self.code.to_string()
    }
}

/// Go's `http.StatusText`.
pub(crate) fn status_text(code: u16) -> &'static str {
    http::StatusCode::from_u16(code)
        .ok()
        .and_then(|c| c.canonical_reason())
        .unwrap_or("")
}

impl fmt::Display for StatusError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{} {}: {}", self.code, status_text(self.code), self.msg)
    }
}

impl std::error::Error for StatusError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&self.typed)
    }
}

/// SdkError is an error of a provider that retries on its own: classified by its HTTP status like a StatusError,
/// never retried by the router.
#[derive(Debug)]
pub struct SdkError {
    pub code: u16,
    pub message: String,
    typed: llm::Error,
}

impl SdkError {
    pub fn new(code: u16, message: impl Into<String>) -> Self {
        let message = message.into();
        SdkError {
            code,
            typed: llm::Error::typed(code.to_string(), message.clone()),
            message,
        }
    }
}

impl fmt::Display for SdkError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for SdkError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&self.typed)
    }
}

/// The StatusError of a response: its status, its body and the wait its Retry-After header asks for.
pub(crate) fn status_error_of(code: u16, headers: &http::HeaderMap, body: &str) -> StatusError {
    let mut e = StatusError::new(code, body);
    if let Some(s) = headers.get("retry-after").and_then(|v| v.to_str().ok())
        && !s.is_empty()
    {
        if let Ok(n) = s.trim().parse::<i64>() {
            if n >= 0 {
                e.retry_after = Duration::from_secs(n as u64);
            }
        } else if let Ok(t) = chrono::DateTime::parse_from_rfc2822(s) {
            let left = t.with_timezone(&chrono::Utc) - chrono::Utc::now();
            e.retry_after = left.to_std().unwrap_or(Duration::ZERO);
        }
    }
    e
}

/// The StatusError in the chain of an error (`errors.As`).
pub fn status_error(err: &llm::Error) -> Option<&StatusError> {
    match err {
        llm::Error::Wrapped { source, .. } => status_error(source),
        llm::Error::Other(b) => b.downcast_ref::<StatusError>(),
        _ => None,
    }
}

/// A network failure (Go's `net.Error`): the connection could not be made, or timed out.
fn is_network(err: &llm::Error) -> bool {
    match err {
        llm::Error::Wrapped { source, .. } => is_network(source),
        llm::Error::Other(b) => b
            .downcast_ref::<reqwest::Error>()
            .is_some_and(|e| e.is_connect() || e.is_timeout()),
        _ => false,
    }
}

/// retryable says whether err is transient, and the wait the provider asked for (zero: none).
pub(crate) fn retryable(err: &llm::Error) -> (bool, Duration) {
    if let Some(se) = status_error(err) {
        return match se.code {
            408 | 409 | 429 | 500 | 502 | 503 | 504 | 529 => (true, se.retry_after),
            _ => (false, Duration::ZERO),
        };
    }
    if err.is_canceled() || err.is_deadline_exceeded() {
        return (false, Duration::ZERO);
    }
    (is_network(err), Duration::ZERO)
}

/// A random number in `[0, n]` (the jitter of the backoff).
pub(crate) fn random_upto(n: u64) -> u64 {
    static SEQ: AtomicU64 = AtomicU64::new(0);
    let mut h = RandomState::new().build_hasher();
    h.write_u64(SEQ.fetch_add(1, Ordering::Relaxed));
    h.finish() % (n + 1)
}

/// The retry settings of a router, copied into a call.
#[derive(Debug, Clone, Copy)]
pub(crate) struct RetryPolicy {
    pub retries: i64,
    pub base: Duration,
}

impl RetryPolicy {
    /// backoff is the wait before the attempt-th retry (0-based).
    pub(crate) fn backoff(&self, attempt: u32, asked: Duration) -> Duration {
        let base = if self.base.is_zero() {
            RETRY_BASE
        } else {
            self.base
        };
        let d = base
            .checked_mul(1u32.checked_shl(attempt).unwrap_or(u32::MAX))
            .unwrap_or(RETRY_CAP)
            .min(RETRY_CAP);
        let half = d / 2;
        // jitter: [d/2, d]
        let d = half + Duration::from_nanos(random_upto(half.as_nanos() as u64));
        d.max(asked.min(RETRY_AFTER_CAP))
    }
}

impl Router {
    pub(crate) fn policy(&self) -> RetryPolicy {
        RetryPolicy {
            retries: self.retries,
            base: self.retry_base,
        }
    }

    /// backoff is the wait before the attempt-th retry (0-based).
    pub fn backoff(&self, attempt: u32, asked: Duration) -> Duration {
        self.policy().backoff(attempt, asked)
    }
}

/// complete_with_retry calls p, retrying the transient failures up to the policy's retries.
pub(crate) async fn complete_with_retry(
    ctx: &Ctx,
    policy: RetryPolicy,
    p: &dyn Provider,
    model: &str,
    req: &llm::Request,
) -> Result<llm::Response, llm::Error> {
    let retries = if policy.retries == 0 {
        DEFAULT_RETRIES
    } else {
        policy.retries
    };
    let mut attempt: u32 = 0;
    loop {
        let res = p.complete(ctx, model, req.clone()).await;
        let err = match res {
            Ok(r) => return Ok(r),
            Err(e) => e,
        };
        let (ok, asked) = retryable(&err);
        if !ok || i64::from(attempt) >= retries {
            return Err(err);
        }
        let wait = policy.backoff(attempt, asked);
        llm::retry_observer_from(ctx)(attempt + 1, &err, wait);
        tokio::select! {
            _ = ctx.done() => return Err(err),
            _ = tokio::time::sleep(wait) => {}
        }
        attempt += 1;
    }
}
