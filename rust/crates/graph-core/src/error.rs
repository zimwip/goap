//! The errors of the graph, with the texts of the Go service (its sentinels `not found`, `invalid`, `conflict`
//! closing a message that says what was not found, ...): the Connect code follows the kind.
use std::fmt;

/// What kind of failure: the sentinel of `contracts/graphapi` it wraps.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    NotFound,
    Invalid,
    Conflict,
    Forbidden,
    /// The Rust service does not serve this request yet: the proxy in mode split replays it on the Go service.
    Unimplemented,
    Internal,
}

/// An error of the graph: its kind and its whole message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GraphError {
    pub kind: Kind,
    pub message: String,
}

impl GraphError {
    pub fn new(kind: Kind, message: impl Into<String>) -> GraphError {
        GraphError {
            kind,
            message: message.into(),
        }
    }

    /// `<what>: not found`.
    pub fn not_found(what: impl fmt::Display) -> GraphError {
        GraphError::new(Kind::NotFound, format!("{what}: not found"))
    }

    /// `<what>: invalid`.
    pub fn invalid(what: impl fmt::Display) -> GraphError {
        GraphError::new(Kind::Invalid, format!("{what}: invalid"))
    }

    /// `<what>: conflict`.
    pub fn conflict(what: impl fmt::Display) -> GraphError {
        GraphError::new(Kind::Conflict, format!("{what}: conflict"))
    }

    pub fn unimplemented(what: impl fmt::Display) -> GraphError {
        GraphError::new(Kind::Unimplemented, what.to_string())
    }

    pub fn internal(what: impl fmt::Display) -> GraphError {
        GraphError::new(Kind::Internal, what.to_string())
    }

    /// The error with `what: ` before its message (Go's `fmt.Errorf("%s: %w", what, err)`).
    pub fn context(mut self, what: impl fmt::Display) -> GraphError {
        self.message = format!("{what}: {}", self.message);
        self
    }

    pub fn is_not_found(&self) -> bool {
        self.kind == Kind::NotFound
    }
}

impl fmt::Display for GraphError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for GraphError {}

pub type Result<T, E = GraphError> = std::result::Result<T, E>;
