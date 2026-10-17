//! Errors of the index.

/// The message of a semantic search with no embedding (Go's ErrSemanticUnavailable).
pub const SEMANTIC_UNAVAILABLE: &str = "semantic search is unavailable: no embedding model answers";
/// The message of a request that is not well formed (Go's ErrInvalid).
pub const INVALID: &str = "invalid search";
/// The message of a similar_to whose document does not exist or may not be seen (Go's ErrNotFound).
pub const NOT_FOUND: &str = "document not found";

/// Errors of a search or a store. The sentinel variants carry the whole message (the sentinel's text, then what the
/// Go code wrapped after it).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Semantic search was asked and no embedding is available (no embedder, the embedding of the query failed, or
    /// the document to compare with has no embedding).
    #[error("{0}")]
    SemanticUnavailable(String),
    /// The request is not well formed.
    #[error("{0}")]
    Invalid(String),
    /// The document of a similar_to does not exist, or the caller may not see it.
    #[error("{0}")]
    NotFound(String),
    /// A database error.
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// Any other failure (an embedder, an authorizer).
    #[error(transparent)]
    Other(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// `fmt.Errorf("%w: %s", ErrSemanticUnavailable, detail)`.
    pub fn semantic_unavailable(detail: impl std::fmt::Display) -> Self {
        Error::SemanticUnavailable(format!("{SEMANTIC_UNAVAILABLE}: {detail}"))
    }

    /// `fmt.Errorf("%w: %s", ErrInvalid, detail)`.
    pub fn invalid(detail: impl std::fmt::Display) -> Self {
        Error::Invalid(format!("{INVALID}: {detail}"))
    }

    /// `fmt.Errorf("%w: %s", ErrNotFound, detail)`.
    pub fn not_found(detail: impl std::fmt::Display) -> Self {
        Error::NotFound(format!("{NOT_FOUND}: {detail}"))
    }

    /// Any error.
    pub fn other(e: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        Error::Other(e.into())
    }

    /// An error of a message.
    pub fn msg(text: impl Into<String>) -> Self {
        Error::Other(text.into().into())
    }

    /// errors.Is(err, ErrSemanticUnavailable).
    pub fn is_semantic_unavailable(&self) -> bool {
        matches!(self, Error::SemanticUnavailable(_))
    }

    /// errors.Is(err, ErrInvalid).
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    /// errors.Is(err, ErrNotFound).
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }
}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::other(e)
    }
}
