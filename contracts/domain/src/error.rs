//! The errors of the package.

/// The text of the `ErrInvalidRef` sentinel.
pub const INVALID_REF: &str = "invalid type reference";

/// An error of the domain model.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A value that does not hold (Go's `fmt.Errorf` of a validation): the message.
    #[error("{0}")]
    Invalid(String),
    /// A malformed type reference (`ErrInvalidRef`): the whole message, ending with [`INVALID_REF`].
    #[error("{0}")]
    InvalidRef(String),
    /// A JSON encoding that failed.
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    /// An error of a use case (a validator registered with `register_item_type`, a decision or review policy).
    #[error(transparent)]
    Other(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// An [`Error::Invalid`] of a message.
    pub fn invalid(msg: impl Into<String>) -> Self {
        Error::Invalid(msg.into())
    }

    /// An [`Error::Other`] wrapping the error of a use case.
    pub fn other(e: impl std::error::Error + Send + Sync + 'static) -> Self {
        Error::Other(Box::new(e))
    }

    /// `errors.Is(err, ErrInvalidRef)`.
    pub fn is_invalid_ref(&self) -> bool {
        matches!(self, Error::InvalidRef(_))
    }

    /// The error of a use case, when it is one of type `E` (`errors.As`).
    pub fn downcast_ref<E: std::error::Error + 'static>(&self) -> Option<&E> {
        match self {
            Error::Other(e) => e.downcast_ref::<E>(),
            _ => None,
        }
    }
}
