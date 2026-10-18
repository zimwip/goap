//! The errors of the graph: the kinds every operation of the graph returns (Go's sentinels `ErrNotFound`,
//! `ErrConflict`, `ErrInvalid`, `ErrNothingToCommit`), shared by the graph, its service, its client and their callers.
//!
//! # How the Go errors map
//!
//! Go returns a sentinel wrapped in a message, `fmt.Errorf("node %s: %w", id, ErrNotFound)`, and its callers ask
//! `errors.Is(err, ErrNotFound)`. In Rust an [`Error`] carries both: its **kind** (the variant, asked with
//! [`Error::is_not_found`], [`Error::is_conflict`], [`Error::is_invalid`], [`Error::is_nothing_to_commit`] or
//! [`Error::kind`], never by matching the text) and its **whole message**, the text Go's `err.Error()` gives (the
//! tests and the web read some of them), which `Display` prints as is.
//!
//! Building one:
//!
//! - [`Error::not_found`], [`Error::conflict`], [`Error::invalid`] take the **whole message**:
//!   `Error::conflict(format!("node {id}: checked out by another flow"))` is Go's `fmt.Errorf("node %s: checked out by
//!   another flow: %w", id, ErrConflict)` minus the sentinel text. When the Go text ends with the sentinel's
//!   (`"...: conflict"`), use [`Error::wrap`]: `Error::wrap(ErrorKind::Conflict, format!("node {id}"))` gives
//!   `"node n1: conflict"`, exactly `fmt.Errorf("node %s: %w", id, ErrConflict)`.
//! - [`Error::sentinel`] is the bare sentinel (`return ErrNotFound`): its message is the sentinel's text
//!   (`"not found"`, `"conflict"`, `"invalid"`, `"nothing to commit: invalid"`).
//! - [`Error::context`] prefixes an error with the context of its caller and keeps its kind and its source, Go's
//!   `fmt.Errorf("load change %s: %w", id, err)`.
//! - [`Error::other`] wraps any other error (storage, transport, a domain error): no kind of the graph, the source kept
//!   for [`Error::downcast_ref`]. `From<goap_domain::Error>` makes one, so `?` works on the domain's results; a domain
//!   error the graph refuses as invalid input is `Error::invalid(e.to_string())` (Go: `fmt.Errorf("%w: %v",
//!   ErrInvalid, err)`), which the caller writes where Go wraps it.
//!
//! [`Error::NothingToCommit`] is an invalid request too (Go's `ErrNothingToCommit` wraps `ErrInvalid`):
//! [`Error::is_invalid`] holds for it, as `errors.Is(err, ErrInvalid)` does.

use std::{fmt, sync::Arc};

/// The text of the `ErrNotFound` sentinel.
pub const NOT_FOUND: &str = "not found";
/// The text of the `ErrConflict` sentinel.
pub const CONFLICT: &str = "conflict";
/// The text of the `ErrInvalid` sentinel.
pub const INVALID: &str = "invalid";
/// The text of the `ErrNothingToCommit` sentinel (it wraps `ErrInvalid`).
pub const NOTHING_TO_COMMIT: &str = "nothing to commit: invalid";

/// The kind of an [`Error`]: which sentinel of the graph it wraps, or none ([`ErrorKind::Other`]).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ErrorKind {
    /// ErrNotFound is returned when an entity does not exist.
    NotFound,
    /// ErrConflict is returned on optimistic concurrency violations.
    Conflict,
    /// ErrInvalid is returned on invalid input.
    Invalid,
    /// ErrNothingToCommit refuses a commit that would write nothing: no accepted modification, nothing to revert (ADR
    /// 0102). It is an invalid request too.
    NothingToCommit,
    /// Any other error (storage, transport, ...).
    Other,
}

impl ErrorKind {
    /// The text of the sentinel of the kind (`"not found"`, ...); empty for [`ErrorKind::Other`].
    pub fn sentinel(self) -> &'static str {
        match self {
            ErrorKind::NotFound => NOT_FOUND,
            ErrorKind::Conflict => CONFLICT,
            ErrorKind::Invalid => INVALID,
            ErrorKind::NothingToCommit => NOTHING_TO_COMMIT,
            ErrorKind::Other => "",
        }
    }
}

/// The source of an [`Error::Other`]: any error, shared so that the error stays `Clone`.
pub type Source = Arc<dyn std::error::Error + Send + Sync>;

/// An error of the graph: its kind (the variant) and its whole message (what Go's `err.Error()` reads). See the module
/// documentation for how the Go errors map.
#[derive(Clone)]
pub enum Error {
    /// An entity that does not exist (`errors.Is(err, ErrNotFound)`).
    NotFound(String),
    /// An optimistic concurrency violation, or a state that refuses the operation (`ErrConflict`).
    Conflict(String),
    /// Invalid input (`ErrInvalid`).
    Invalid(String),
    /// A commit that would write nothing (`ErrNothingToCommit`, an invalid request too).
    NothingToCommit(String),
    /// Any other error: its message (the context of the callers included) and the error it comes from, if any.
    Other {
        message: String,
        source: Option<Source>,
    },
}

impl Error {
    /// An error of a kind with its whole message. [`ErrorKind::Other`] makes an [`Error::Other`] with no source.
    pub fn new(kind: ErrorKind, message: impl Into<String>) -> Self {
        let message = message.into();
        match kind {
            ErrorKind::NotFound => Error::NotFound(message),
            ErrorKind::Conflict => Error::Conflict(message),
            ErrorKind::Invalid => Error::Invalid(message),
            ErrorKind::NothingToCommit => Error::NothingToCommit(message),
            ErrorKind::Other => Error::Other {
                message,
                source: None,
            },
        }
    }

    /// The bare sentinel of a kind (Go's `return ErrConflict`): its message is the sentinel's text.
    pub fn sentinel(kind: ErrorKind) -> Self {
        Error::new(kind, kind.sentinel())
    }

    /// Go's `fmt.Errorf("<context>: %w", ErrX)`: the message is the context followed by the sentinel's text
    /// (`wrap(ErrorKind::NotFound, "node n1")` reads `"node n1: not found"`).
    pub fn wrap(kind: ErrorKind, context: impl fmt::Display) -> Self {
        if kind == ErrorKind::Other {
            return Error::new(kind, context.to_string());
        }
        Error::new(kind, format!("{context}: {}", kind.sentinel()))
    }

    /// An [`Error::NotFound`] with its whole message.
    pub fn not_found(message: impl Into<String>) -> Self {
        Error::NotFound(message.into())
    }

    /// An [`Error::Conflict`] with its whole message.
    pub fn conflict(message: impl Into<String>) -> Self {
        Error::Conflict(message.into())
    }

    /// An [`Error::Invalid`] with its whole message.
    pub fn invalid(message: impl Into<String>) -> Self {
        Error::Invalid(message.into())
    }

    /// The `ErrNothingToCommit` sentinel (`"nothing to commit: invalid"`).
    pub fn nothing_to_commit() -> Self {
        Error::sentinel(ErrorKind::NothingToCommit)
    }

    /// An [`Error::Other`] wrapping any error: its message is the error's, its source the error.
    pub fn other(e: impl std::error::Error + Send + Sync + 'static) -> Self {
        Error::Other {
            message: e.to_string(),
            source: Some(Arc::new(e)),
        }
    }

    /// An [`Error::Other`] of a message alone (Go's `errors.New` / `fmt.Errorf` without `%w`).
    pub fn msg(message: impl Into<String>) -> Self {
        Error::new(ErrorKind::Other, message)
    }

    /// Go's `fmt.Errorf("<context>: %w", err)`: the same error, its message prefixed with the context, its kind and
    /// source kept.
    pub fn context(self, context: impl fmt::Display) -> Self {
        let prefix = |m: String| format!("{context}: {m}");
        match self {
            Error::NotFound(m) => Error::NotFound(prefix(m)),
            Error::Conflict(m) => Error::Conflict(prefix(m)),
            Error::Invalid(m) => Error::Invalid(prefix(m)),
            Error::NothingToCommit(m) => Error::NothingToCommit(prefix(m)),
            Error::Other { message, source } => Error::Other {
                message: prefix(message),
                source,
            },
        }
    }

    /// The kind of the error.
    pub fn kind(&self) -> ErrorKind {
        match self {
            Error::NotFound(_) => ErrorKind::NotFound,
            Error::Conflict(_) => ErrorKind::Conflict,
            Error::Invalid(_) => ErrorKind::Invalid,
            Error::NothingToCommit(_) => ErrorKind::NothingToCommit,
            Error::Other { .. } => ErrorKind::Other,
        }
    }

    /// The whole message (what `Display` prints).
    pub fn message(&self) -> &str {
        match self {
            Error::NotFound(m)
            | Error::Conflict(m)
            | Error::Invalid(m)
            | Error::NothingToCommit(m) => m,
            Error::Other { message, .. } => message,
        }
    }

    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }

    /// `errors.Is(err, ErrConflict)`.
    pub fn is_conflict(&self) -> bool {
        matches!(self, Error::Conflict(_))
    }

    /// `errors.Is(err, ErrInvalid)`: true of [`Error::NothingToCommit`] too.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_) | Error::NothingToCommit(_))
    }

    /// `errors.Is(err, ErrNothingToCommit)`.
    pub fn is_nothing_to_commit(&self) -> bool {
        matches!(self, Error::NothingToCommit(_))
    }

    /// The source of an [`Error::Other`], when it is of type `E` (Go's `errors.As`).
    pub fn downcast_ref<E: std::error::Error + 'static>(&self) -> Option<&E> {
        match self {
            Error::Other {
                source: Some(s), ..
            } => s.downcast_ref::<E>(),
            _ => None,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.message())
    }
}

impl fmt::Debug for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?}({:?})", self.kind(), self.message())
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Other {
                source: Some(s), ..
            } => Some(s.as_ref() as &(dyn std::error::Error + 'static)),
            _ => None,
        }
    }
}

impl PartialEq for Error {
    /// Two errors are equal when their kinds and messages are (the sources are not compared).
    fn eq(&self, other: &Self) -> bool {
        self.kind() == other.kind() && self.message() == other.message()
    }
}

impl From<goap_domain::Error> for Error {
    fn from(e: goap_domain::Error) -> Self {
        Error::other(e)
    }
}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::other(e)
    }
}

impl From<goap_ctx::CtxError> for Error {
    fn from(e: goap_ctx::CtxError) -> Self {
        Error::other(e)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sentinels_and_wrapping() {
        assert_eq!(
            Error::sentinel(ErrorKind::NotFound).to_string(),
            "not found"
        );
        assert_eq!(
            Error::nothing_to_commit().to_string(),
            "nothing to commit: invalid"
        );
        assert!(Error::nothing_to_commit().is_invalid());
        assert!(Error::nothing_to_commit().is_nothing_to_commit());
        assert!(!Error::invalid("x").is_nothing_to_commit());
        let e = Error::wrap(ErrorKind::Conflict, "node n1");
        assert_eq!(e.to_string(), "node n1: conflict");
        assert!(e.is_conflict());
        let e = e.context("checkout");
        assert_eq!(e.to_string(), "checkout: node n1: conflict");
        assert!(e.is_conflict() && !e.is_invalid());
        let e = Error::from(goap_domain::Error::invalid("bad")).context("load");
        assert_eq!(e.to_string(), "load: bad");
        assert_eq!(e.kind(), ErrorKind::Other);
        assert!(e.downcast_ref::<goap_domain::Error>().is_some());
        assert!(std::error::Error::source(&e).is_some());
    }
}
