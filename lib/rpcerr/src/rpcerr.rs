use std::error::Error as StdError;

use goap_connect::{Code, ConnectError};
use goap_ctx::CtxError;
use goap_graphapi::Error as GraphError;

/// The text of [`NotFound`]'s bare sentinel.
pub const NOT_FOUND: &str = "not found";

/// ErrNotFound is a generic not-found error for services without their own; its message is the whole text
/// ([`NotFound::sentinel`] is the bare `"not found"`, [`NotFound::wrap`] Go's `fmt.Errorf("<context>: %w",
/// ErrNotFound)`).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct NotFound(pub String);

impl NotFound {
    /// The bare sentinel: `"not found"`.
    pub fn sentinel() -> Self {
        NotFound(NOT_FOUND.to_string())
    }

    /// `"<context>: not found"`.
    pub fn wrap(context: impl std::fmt::Display) -> Self {
        NotFound(format!("{context}: {NOT_FOUND}"))
    }
}

impl Default for NotFound {
    fn default() -> Self {
        NotFound::sentinel()
    }
}

/// ReasonHeader carries, on a Connect error, the domain sentinel finer than its code (reasons), so that a remote
/// caller tells them apart as an in-process one does.
pub const REASON_HEADER: &str = "Goap-Error-Reason";

/// The reason `ErrNothingToCommit` travels under (the only reason).
const NOTHING_TO_COMMIT: &str = "nothing-to-commit";

/// The errors of the chain of `err`, outermost first (Go's `errors.Is` walk). A review error of the port is
/// transparent in Rust (its `source` skips the port's error), so the port's error is visited explicitly.
fn chain<'a>(err: &'a (dyn StdError + 'static)) -> Vec<&'a (dyn StdError + 'static)> {
    let mut out = Vec::new();
    let mut cur = Some(err);
    while let Some(e) = cur {
        out.push(e);
        if let Some(goap_review::Error::Port(inner)) = e.downcast_ref::<goap_review::Error>() {
            cur = Some(inner.as_ref());
            continue;
        }
        cur = e.source();
    }
    out
}

fn any_of(c: &[&(dyn StdError + 'static)], f: impl Fn(&(dyn StdError + 'static)) -> bool) -> bool {
    c.iter().any(|e| f(*e))
}

fn graph<'a>(e: &'a (dyn StdError + 'static)) -> Option<&'a GraphError> {
    e.downcast_ref::<GraphError>()
}

fn review<'a>(e: &'a (dyn StdError + 'static)) -> Option<&'a goap_review::Error> {
    e.downcast_ref::<goap_review::Error>()
}

fn forbidden(e: &(dyn StdError + 'static)) -> bool {
    e.downcast_ref::<goap_authz::Error>()
        .is_some_and(goap_authz::Error::is_forbidden)
        || review(e).is_some_and(goap_review::Error::is_forbidden)
}

/// ToConnect wraps err with the matching Connect code: a [`ConnectError`] in its chain is returned as is, a
/// forbidden one is `permission_denied`, a not-found (the graph's, [`NotFound`], the review's) `not_found`, a
/// conflict `failed_precondition`, an invalid request `invalid_argument` (with the [`REASON_HEADER`] of a nothing to
/// commit), a cancelled or expired context `canceled` / `deadline_exceeded`, anything else `internal`. The message is
/// the error's text, and the error stays the source of the Connect error ([`ConnectError::source_as`]).
pub fn to_connect<E>(err: E) -> ConnectError
where
    E: Into<Box<dyn StdError + Send + Sync + 'static>>,
{
    let err: Box<dyn StdError + Send + Sync + 'static> = err.into();
    let err = match err.downcast::<ConnectError>() {
        Ok(ce) => return *ce,
        Err(e) => e,
    };
    let code = {
        let c = chain(err.as_ref());
        if let Some(ce) = c.iter().find_map(|e| e.downcast_ref::<ConnectError>()) {
            return ce.clone();
        }
        if any_of(&c, forbidden) {
            Some((Code::PermissionDenied, false))
        } else if any_of(&c, |e| {
            graph(e).is_some_and(GraphError::is_not_found)
                || e.downcast_ref::<NotFound>().is_some()
                || review(e).is_some_and(goap_review::Error::is_not_found)
        }) {
            Some((Code::NotFound, false))
        } else if any_of(&c, |e| {
            graph(e).is_some_and(GraphError::is_conflict)
                || review(e).is_some_and(goap_review::Error::is_conflict)
        }) {
            Some((Code::FailedPrecondition, false))
        } else if any_of(&c, |e| {
            graph(e).is_some_and(GraphError::is_invalid)
                || review(e).is_some_and(goap_review::Error::is_invalid)
        }) {
            let nothing = any_of(&c, |e| {
                graph(e).is_some_and(GraphError::is_nothing_to_commit)
            });
            Some((Code::InvalidArgument, nothing))
        } else if any_of(&c, |e| {
            matches!(e.downcast_ref::<CtxError>(), Some(CtxError::Canceled))
        }) {
            Some((Code::Canceled, false))
        } else if any_of(&c, |e| {
            matches!(
                e.downcast_ref::<CtxError>(),
                Some(CtxError::DeadlineExceeded)
            )
        }) {
            Some((Code::DeadlineExceeded, false))
        } else {
            None
        }
    };
    match code {
        Some((code, nothing_to_commit)) => {
            let ce = ConnectError::from_boxed(code, err);
            if nothing_to_commit {
                ce.with_meta(REASON_HEADER, NOTHING_TO_COMMIT)
            } else {
                ce
            }
        }
        None => ConnectError::from_boxed(Code::Internal, err),
    }
}

/// FromConnect maps Connect codes back to domain errors so that remote adapters behave like in-process
/// implementations: `not_found` is the graph's not-found, `failed_precondition` its conflict, `invalid_argument` its
/// invalid request (a nothing to commit when the [`REASON_HEADER`] says so), `permission_denied` an error whose
/// source is `authz`'s forbidden ([`is_forbidden`]); anything else keeps the Connect error as its source.
///
/// The messages are Go's `errors.Join(sentinel, err)`: the sentinel's text, a new line, the Connect error
/// (`"not found\nnot_found: node n1: not found"`).
pub fn from_connect(err: ConnectError) -> GraphError {
    let text = err.to_string();
    let nothing_to_commit =
        err.meta().get(REASON_HEADER).and_then(|v| v.to_str().ok()) == Some(NOTHING_TO_COMMIT);
    // the reason joins first, then the code's sentinel: errors.Join(code sentinel, errors.Join(reason, err))
    let joined = if nothing_to_commit {
        format!("{}\n{text}", goap_graphapi::NOTHING_TO_COMMIT)
    } else {
        text.clone()
    };
    match err.code() {
        Code::PermissionDenied => {
            let f = goap_authz::Error::Forbidden(joined);
            GraphError::Other {
                message: f.to_string(),
                source: Some(std::sync::Arc::new(f)),
            }
        }
        Code::NotFound => GraphError::NotFound(format!("{}\n{joined}", goap_graphapi::NOT_FOUND)),
        Code::FailedPrecondition => {
            GraphError::Conflict(format!("{}\n{joined}", goap_graphapi::CONFLICT))
        }
        Code::InvalidArgument => {
            let message = format!("{}\n{joined}", goap_graphapi::INVALID);
            if nothing_to_commit {
                GraphError::NothingToCommit(message)
            } else {
                GraphError::Invalid(message)
            }
        }
        _ => {
            if nothing_to_commit {
                // Go keeps the reason without an invalid sentinel: errors.Is(err, ErrNothingToCommit) holds.
                GraphError::NothingToCommit(joined)
            } else {
                GraphError::Other {
                    message: text,
                    source: Some(std::sync::Arc::new(err)),
                }
            }
        }
    }
}

/// `errors.Is(err, authz.ErrForbidden)` of an error [`from_connect`] returned (or any graph error wrapping a
/// forbidden one).
pub fn is_forbidden(err: &GraphError) -> bool {
    chain(err).into_iter().any(forbidden)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn domain_errors_map_to_codes() {
        let cases: Vec<(Box<dyn StdError + Send + Sync>, Code)> = vec![
            (
                Box::new(goap_authz::Error::Forbidden("alice may not".into())),
                Code::PermissionDenied,
            ),
            (
                Box::new(GraphError::wrap(
                    goap_graphapi::ErrorKind::NotFound,
                    "node n1",
                )),
                Code::NotFound,
            ),
            (
                Box::new(NotFound::wrap("preferences of bob")),
                Code::NotFound,
            ),
            (
                Box::new(goap_review::Error::NotFound("r1".into())),
                Code::NotFound,
            ),
            (
                Box::new(goap_review::Error::Forbidden("r1".into())),
                Code::PermissionDenied,
            ),
            (
                Box::new(GraphError::conflict("moved")),
                Code::FailedPrecondition,
            ),
            (
                Box::new(goap_review::Error::Conflict("x".into())),
                Code::FailedPrecondition,
            ),
            (Box::new(GraphError::invalid("bad")), Code::InvalidArgument),
            (
                Box::new(goap_review::Error::Invalid("x".into())),
                Code::InvalidArgument,
            ),
            (Box::new(CtxError::Canceled), Code::Canceled),
            (Box::new(CtxError::DeadlineExceeded), Code::DeadlineExceeded),
            (Box::new(GraphError::msg("disk full")), Code::Internal),
            (
                Box::new(goap_review::Error::port(GraphError::conflict("port"))),
                Code::FailedPrecondition,
            ),
            (
                Box::new(GraphError::other(CtxError::Canceled)),
                Code::Canceled,
            ),
        ];
        for (err, want) in cases {
            let text = err.to_string();
            let ce = to_connect(err);
            assert_eq!(ce.code(), want, "{text}");
            assert_eq!(ce.message(), text);
        }
        let ce = ConnectError::aborted("decided");
        assert_eq!(to_connect(ce.clone()).code(), Code::Aborted);
        assert_eq!(to_connect(GraphError::other(ce)).code(), Code::Aborted);
        let ce = to_connect(GraphError::invalid("x"));
        assert!(ce.meta().get(REASON_HEADER).is_none());
        assert!(
            ce.source_as::<GraphError>()
                .is_some_and(GraphError::is_invalid)
        );
    }

    #[test]
    fn nothing_to_commit_travels_with_its_reason() {
        let ce = to_connect(GraphError::nothing_to_commit().context("commit c1"));
        assert_eq!(ce.code(), Code::InvalidArgument);
        assert_eq!(ce.meta().get(REASON_HEADER).unwrap(), "nothing-to-commit");
        let back = from_connect(ce);
        assert!(back.is_nothing_to_commit() && back.is_invalid());
        assert_eq!(
            back.to_string(),
            "invalid\nnothing to commit: invalid\ninvalid_argument: commit c1: nothing to commit: invalid"
        );
    }

    #[test]
    fn connect_codes_map_back() {
        let e = from_connect(ConnectError::not_found("node n1: not found"));
        assert!(e.is_not_found());
        assert_eq!(e.to_string(), "not found\nnot_found: node n1: not found");
        assert!(from_connect(ConnectError::failed_precondition("x")).is_conflict());
        let e = from_connect(ConnectError::invalid_argument("x"));
        assert!(e.is_invalid() && !e.is_nothing_to_commit());
        let e = from_connect(ConnectError::permission_denied("alice may not"));
        assert!(is_forbidden(&e));
        assert_eq!(
            e.to_string(),
            "permission denied\npermission_denied: alice may not"
        );
        let e = from_connect(ConnectError::unavailable("down"));
        assert_eq!(e.kind(), goap_graphapi::ErrorKind::Other);
        assert!(!is_forbidden(&e));
        assert_eq!(e.to_string(), "unavailable: down");
        assert_eq!(
            e.downcast_ref::<ConnectError>().unwrap().code(),
            Code::Unavailable
        );
        // a round trip keeps the code
        let ce = to_connect(from_connect(ConnectError::permission_denied("no")));
        assert_eq!(ce.code(), Code::PermissionDenied);
        let ce = to_connect(from_connect(ConnectError::not_found("no")));
        assert_eq!(ce.code(), Code::NotFound);
    }
}
