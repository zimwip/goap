//! The errors of the methodology registry: Go's sentinels `ErrNotFound`, `ErrImmutable`, `ErrInvalid`, `ErrSuspended`
//! and what they wrap.

use std::sync::Arc;

/// The text of `ErrNotFound`: an unknown methodology.
pub const ERR_NOT_FOUND: &str = "methodology not found";
/// The text of `ErrImmutable`: a published or archived version cannot be modified.
pub const ERR_IMMUTABLE: &str = "methodology version is not a draft";
/// The text of `ErrInvalid`: validation issues.
pub const ERR_INVALID: &str = "invalid methodology";
/// The text of `ErrSuspended`: a published methodology version suspended by a change of the domains it follows.
pub const ERR_SUSPENDED: &str = "methodology version is suspended";

/// A boxed error of a seam (the domain registry, a publisher, a callback).
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// suspendedError is ErrSuspended for one version, with the refusal of the compiler (ADR 0125 §3).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SuspendedError {
    /// `name@version`.
    pub r#ref: String,
    /// The refusal of the compiler (its text).
    pub cause: String,
    /// Whether it also wraps `graphapi.ErrInvalid` (MethodologyVersion: no change starts on it).
    pub graph_invalid: bool,
}

impl std::fmt::Display for SuspendedError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "methodology {} is suspended: it no longer compiles against the domains it follows: {}",
            self.r#ref, self.cause
        )?;
        if self.graph_invalid {
            write!(f, ": {}", goap_graphapi::INVALID)?;
        }
        Ok(())
    }
}

impl std::error::Error for SuspendedError {}

/// Error of the service, its stores and its clients. The sentinel variants hold the whole message, as Go's
/// `fmt.Errorf("...: %w", ErrX)` writes it.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// `ErrNotFound`: the message ends with ": methodology not found".
    #[error("{0}")]
    NotFound(String),
    /// `ErrImmutable`: the message ends with ": methodology version is not a draft".
    #[error("{0}")]
    Immutable(String),
    /// `ErrInvalid`: the message holds "invalid methodology".
    #[error("{0}")]
    Invalid(String),
    /// An `ErrInvalid` of an import asked to publish a methodology with issues: it was kept as a draft (`record`), and
    /// the issues come with the error (Go returns the three).
    #[error("{message}")]
    Draft {
        record: Box<crate::store::Record>,
        issues: goap_def::Issues,
        message: String,
    },
    /// `ErrSuspended` (suspendedError).
    #[error(transparent)]
    Suspended(SuspendedError),
    /// `engine.ErrUnknownMethodology`: `unknown methodology <ref>`.
    #[error("unknown methodology {0}")]
    UnknownMethodology(String),
    /// The caller may not (`authz.ErrForbidden` and the other errors of the authorizer).
    #[error(transparent)]
    Authz(#[from] goap_authz::Error),
    /// An error of the graph (the store over the graph, `graphapi.ErrInvalid` of MethodologyVersion, ...).
    #[error(transparent)]
    Graph(#[from] goap_graphapi::Error),
    /// The refusal of the compiler or of the type catalogue (cached by the compile cache, hence shared).
    #[error(transparent)]
    Compile(Arc<dyn std::error::Error + Send + Sync>),
    /// The domain registry could not be read.
    #[error(transparent)]
    Domains(BoxError),
    /// A condition that does not evaluate (a guard, a gate, a guardian).
    #[error(transparent)]
    Condition(#[from] goap_condition::Error),
    /// A Connect call of the client.
    #[error(transparent)]
    Rpc(#[from] goap_connect::ConnectError),
    /// A file that cannot be read (Seed).
    #[error(transparent)]
    Io(#[from] std::io::Error),
    /// Any other failure, with its whole message.
    #[error("{0}")]
    Other(String),
}

/// The registry's results.
pub type Result<T, E = Error> = std::result::Result<T, E>;

impl Error {
    /// `fmt.Errorf("%s: %w", what, ErrNotFound)`.
    pub fn not_found(what: impl std::fmt::Display) -> Error {
        Error::NotFound(format!("{what}: {ERR_NOT_FOUND}"))
    }

    /// `fmt.Errorf("%s: %w", what, ErrImmutable)`.
    pub fn immutable(what: impl std::fmt::Display) -> Error {
        Error::Immutable(format!("{what}: {ERR_IMMUTABLE}"))
    }

    /// `fmt.Errorf("%s: %w", what, ErrInvalid)`.
    pub fn invalid(what: impl std::fmt::Display) -> Error {
        Error::Invalid(format!("{what}: {ERR_INVALID}"))
    }

    /// `fmt.Errorf("%w: %s", ErrInvalid, detail)`.
    pub fn invalid_because(detail: impl std::fmt::Display) -> Error {
        Error::Invalid(format!("{ERR_INVALID}: {detail}"))
    }

    /// An error of the compiler or the type catalogue.
    pub fn compile(e: impl std::error::Error + Send + Sync + 'static) -> Error {
        Error::Compile(Arc::new(e))
    }

    /// An error of the domain registry.
    pub fn domains(e: impl Into<BoxError>) -> Error {
        Error::Domains(e.into())
    }

    /// `fmt.Errorf("<prefix>: %w", err)`: the sentinel kind is kept.
    pub fn context(self, prefix: impl std::fmt::Display) -> Error {
        match self {
            Error::NotFound(m) => Error::NotFound(format!("{prefix}: {m}")),
            Error::Immutable(m) => Error::Immutable(format!("{prefix}: {m}")),
            Error::Invalid(m) => Error::Invalid(format!("{prefix}: {m}")),
            Error::Graph(e) => Error::Graph(e.context(prefix.to_string())),
            e => Error::Other(format!("{prefix}: {e}")),
        }
    }

    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }

    /// `errors.Is(err, ErrImmutable)`.
    pub fn is_immutable(&self) -> bool {
        matches!(self, Error::Immutable(_))
    }

    /// `errors.Is(err, ErrInvalid)` (the registry's sentinel).
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_) | Error::Draft { .. })
    }

    /// `errors.Is(err, ErrSuspended)`.
    pub fn is_suspended(&self) -> bool {
        matches!(self, Error::Suspended(_))
    }

    /// `errors.Is(err, engine.ErrUnknownMethodology)`.
    pub fn is_unknown_methodology(&self) -> bool {
        matches!(self, Error::UnknownMethodology(_))
    }

    /// `errors.Is(err, authz.ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Authz(e) if e.is_forbidden())
    }

    /// `errors.Is(err, graphapi.ErrInvalid)`.
    pub fn is_graph_invalid(&self) -> bool {
        match self {
            Error::Graph(e) => e.is_invalid(),
            Error::Suspended(s) => s.graph_invalid,
            _ => false,
        }
    }

    /// `errors.Is(err, graphapi.ErrConflict)`.
    pub fn is_conflict(&self) -> bool {
        matches!(self, Error::Graph(e) if e.is_conflict())
    }

    /// The error as the graph sees it (an implementation of a graph seam over the registry): a graph error is
    /// carried as it is, a suspension refusing a change as `graphapi.ErrInvalid`, an unknown methodology as
    /// not found, anything else by its message.
    pub fn into_graph(self) -> goap_graphapi::Error {
        match self {
            Error::Graph(e) => e,
            Error::Suspended(s) if s.graph_invalid => goap_graphapi::Error::invalid(s.to_string()),
            Error::NotFound(m) => goap_graphapi::Error::not_found(m),
            Error::Invalid(m) => goap_graphapi::Error::invalid(m),
            Error::Draft { message, .. } => goap_graphapi::Error::invalid(message),
            e => goap_graphapi::Error::msg(e.to_string()),
        }
    }

    /// The error as the engine sees it (engine.MethodologyPort).
    pub fn into_engine(self) -> goap_engine::Error {
        match self {
            Error::UnknownMethodology(r) => goap_engine::unknown_methodology(&r),
            Error::Graph(e) => goap_engine::Error::Graph(e),
            e => goap_engine::Error::msg(e.to_string()),
        }
    }
}

impl From<goap_methodology::Error> for Error {
    fn from(e: goap_methodology::Error) -> Self {
        Error::compile(e)
    }
}

impl From<goap_typecat::Error> for Error {
    fn from(e: goap_typecat::Error) -> Self {
        Error::compile(e)
    }
}

impl From<goap_domainclient::Error> for Error {
    fn from(e: goap_domainclient::Error) -> Self {
        Error::domains(e)
    }
}
