//! The errors of the gateway: the policy applied to completions and the administration (Go's sentinels `ErrForbidden`,
//! `ErrQuotaExceeded`, `ErrModelDisabled`, `ErrInvalid`, `ErrNotFound`), with what failed below them.

use goap_connect::ConnectError;
use goap_llm as llm;

/// Any error of a seam (a secret resolver, the vault, a session function).
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// `fmt.Errorf("%w: %s", ErrX, detail)`: the sentinel's text, then its detail when there is one.
fn kind_text(kind: &str, detail: &str) -> String {
    if detail.is_empty() {
        kind.to_string()
    } else {
        format!("{kind}: {detail}")
    }
}

/// The errors of the package. The policy variants carry the detail Go wraps after its sentinel (their text is
/// `"<sentinel>: <detail>"`, the bare sentinel when the detail is empty).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrForbidden: the caller may not use the model, or read the calls.
    #[error("{}", kind_text("forbidden", .0))]
    Forbidden(String),
    /// ErrQuotaExceeded: the token budget of the model is spent.
    #[error("{}", kind_text("quota exceeded", .0))]
    QuotaExceeded(String),
    /// ErrModelDisabled: the model is not in the catalog, disabled, or not usable here.
    #[error("{}", kind_text("model not available", .0))]
    ModelDisabled(String),
    /// ErrInvalid: the request is invalid.
    #[error("{}", kind_text("invalid", .0))]
    Invalid(String),
    /// ErrNotFound: no such call, or no exchange stored for it.
    #[error("{}", kind_text("not found", .0))]
    NotFound(String),
    /// An error wrapped with a message (`fmt.Errorf("msg: %w", err)`).
    #[error("{message}: {source}")]
    Context {
        message: String,
        #[source]
        source: Box<Error>,
    },
    /// A provider, the router or a model call failed.
    #[error(transparent)]
    Llm(#[from] llm::Error),
    /// A call of the gateway service over Connect failed.
    #[error(transparent)]
    Rpc(#[from] ConnectError),
    /// The store failed.
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// Any other failure, its message as Go writes it.
    #[error("{0}")]
    Message(String),
}

impl Error {
    /// The error wrapped with a message: `fmt.Errorf("<message>: %w", err)`.
    pub fn context(self, message: impl Into<String>) -> Error {
        Error::Context {
            message: message.into(),
            source: Box::new(self),
        }
    }

    /// The error behind the wrappings.
    pub fn root(&self) -> &Error {
        match self {
            Error::Context { source, .. } => source.root(),
            e => e,
        }
    }

    /// `errors.Is(err, ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        matches!(self.root(), Error::Forbidden(_))
    }

    /// `errors.Is(err, ErrQuotaExceeded)`.
    pub fn is_quota_exceeded(&self) -> bool {
        matches!(self.root(), Error::QuotaExceeded(_))
    }

    /// `errors.Is(err, ErrModelDisabled)`.
    pub fn is_model_disabled(&self) -> bool {
        matches!(self.root(), Error::ModelDisabled(_))
    }

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self.root(), Error::Invalid(_))
    }

    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self.root(), Error::NotFound(_))
    }

    /// Whether the error is one of the policy (forbidden, quota, disabled, invalid): the handler answers those with
    /// their code, anything else as unavailable.
    pub fn is_policy(&self) -> bool {
        self.is_forbidden()
            || self.is_quota_exceeded()
            || self.is_model_disabled()
            || self.is_invalid()
    }

    /// The error as an error of the completion contract (`llm::Client`): a provider error as it is, any other wrapped.
    pub fn into_llm(self) -> llm::Error {
        match self {
            Error::Llm(e) => e,
            e => llm::Error::Other(Box::new(e)),
        }
    }
}
