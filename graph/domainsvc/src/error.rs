//! The errors of the domain registry (Go's sentinels `ErrNotFound`, `ErrImmutable`, `ErrInvalid` and what they wrap).

/// The text of `ErrNotFound`: an unknown domain version.
pub const ERR_NOT_FOUND: &str = "domain not found";
/// The text of `ErrImmutable`: a published or archived version, or a built-in domain, cannot be modified.
pub const ERR_IMMUTABLE: &str = "domain version is not a draft";
/// The text of `ErrInvalid`: validation issues.
pub const ERR_INVALID: &str = "invalid domain";

/// A boxed error of a seam (a publisher).
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// Error of the service and its stores. The sentinel variants hold the whole message, as Go's
/// `fmt.Errorf("...: %w", ErrX)` writes it.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// `ErrNotFound`: the message ends with ": domain not found".
    #[error("{0}")]
    NotFound(String),
    /// `ErrImmutable`: a version that is not a draft, or a built-in domain (whose message says so).
    #[error("{0}")]
    Immutable(String),
    /// `ErrInvalid`: the message holds "invalid domain".
    #[error("{0}")]
    Invalid(String),
    /// An `ErrInvalid` of an import asked to publish a domain with issues: the domain was kept as a draft (`record`),
    /// and the issues are returned with the error (Go returns the three).
    #[error("{message}")]
    Draft {
        record: Box<goap_def::Record>,
        issues: goap_def::Issues,
        message: String,
    },
    /// The caller may not (`authz.ErrForbidden` and the other errors of the authorizer).
    #[error(transparent)]
    Authz(#[from] goap_authz::Error),
    /// The types of the domains in force do not resolve (the error of the type catalogue, unwrapped as in Go).
    #[error(transparent)]
    Types(#[from] goap_typecat::Error),
    /// A script failed to run (`dsl.RunAlgorithm`).
    #[error(transparent)]
    Script(#[from] goap_dsl::Error),
    /// A database error.
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// A file of the seed directory could not be read.
    #[error(transparent)]
    Io(#[from] std::io::Error),
    /// Any other failure, with its whole message (a stored definition that does not decode, a file that does not
    /// parse, a render).
    #[error("{0}")]
    Other(String),
}

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

    /// `fmt.Errorf("%w: %v", ErrInvalid, detail)`.
    pub fn invalid_because(detail: impl std::fmt::Display) -> Error {
        Error::Invalid(format!("{ERR_INVALID}: {detail}"))
    }

    /// `fmt.Errorf("%s: %w", prefix, err)`: the kind is kept, the message prefixed.
    pub fn context(self, prefix: impl std::fmt::Display) -> Error {
        match self {
            Error::NotFound(m) => Error::NotFound(format!("{prefix}: {m}")),
            Error::Immutable(m) => Error::Immutable(format!("{prefix}: {m}")),
            Error::Invalid(m) => Error::Invalid(format!("{prefix}: {m}")),
            Error::Draft {
                record,
                issues,
                message,
            } => Error::Draft {
                record,
                issues,
                message: format!("{prefix}: {message}"),
            },
            Error::Authz(goap_authz::Error::Forbidden(m)) => {
                Error::Authz(goap_authz::Error::Forbidden(format!("{prefix}: {m}")))
            }
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

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_) | Error::Draft { .. })
    }

    /// `errors.Is(err, authz.ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Authz(e) if e.is_forbidden())
    }
}

impl From<goap_def::Error> for Error {
    fn from(e: goap_def::Error) -> Self {
        match e {
            goap_def::Error::Io(e) => Error::Io(e),
            e => Error::Other(e.to_string()),
        }
    }
}
