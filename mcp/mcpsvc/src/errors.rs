//! The errors of the hub (`errors.go`, and `ErrNotFound` of `store.go`).

use goap_connect::ConnectError;

/// The text of Go's `ErrNotFound`.
pub const ERR_NOT_FOUND: &str = "not found";
/// The text of Go's `ErrNotBound`: the organization does not bind the MCP of a tool.
pub const ERR_NOT_BOUND: &str = "mcp not bound";
/// The text of Go's `ErrAdapterDef`: the definition an adapter instance refers to is missing or does not fit.
pub const ERR_ADAPTER_DEF: &str = "adapter definition";
/// The text of Go's `ErrUnavailable`: the connector of a binding is not registered or its lease expired.
pub const ERR_UNAVAILABLE: &str = "connector unavailable";

/// ToolError is a failure reported by the connector (or the adapter's code) for one operation.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{msg}")]
pub struct ToolError {
    pub msg: String,
}

/// The errors of the package. Each sentinel of the Go package is a variant holding the whole message (the sentinel's
/// text included, as Go's `%w` wrapping prints it); `errors.Is` is a method (`is_not_found`, ...).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// An unknown connector, MCP or tool (`ErrNotFound`): `<what>: not found`.
    #[error("{0}")]
    NotFound(String),
    /// The organization does not bind the MCP of a tool (`ErrNotBound`).
    #[error("{0}")]
    NotBound(String),
    /// The definition an adapter instance refers to is missing or does not fit (`ErrAdapterDef`).
    #[error("{0}")]
    AdapterDef(String),
    /// The connector is not registered, its lease expired or it could not be reached (`ErrUnavailable`).
    #[error("{0}")]
    Unavailable(String),
    /// A malformed request or definition (`mcp.ErrInvalid`): the message ends with `: invalid`.
    #[error("{0}")]
    Invalid(String),
    /// A failure reported by the connector or the adapter's code.
    #[error(transparent)]
    Tool(#[from] ToolError),
    /// A failure of the graph read by the directory.
    #[error(transparent)]
    Graph(#[from] goap_graphapi::Error),
    /// A failure of a Connect call (hub client, connector over the network).
    #[error(transparent)]
    Rpc(#[from] ConnectError),
    /// A failure of the SQL store.
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// A stored registration that does not decode, or one that cannot be encoded.
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    /// The model token of a call could not be minted.
    #[error(transparent)]
    ModelToken(#[from] goap_modeltoken::Error),
    /// Any other failure, by its whole message.
    #[error("{0}")]
    Message(String),
}

impl Error {
    /// `fmt.Errorf("%s: %w", what, ErrNotFound)`.
    pub fn not_found(what: impl std::fmt::Display) -> Error {
        Error::NotFound(format!("{what}: {ERR_NOT_FOUND}"))
    }

    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }

    /// `errors.Is(err, ErrNotBound)`.
    pub fn is_not_bound(&self) -> bool {
        matches!(self, Error::NotBound(_))
    }

    /// `errors.Is(err, ErrAdapterDef)`.
    pub fn is_adapter_def(&self) -> bool {
        matches!(self, Error::AdapterDef(_))
    }

    /// `errors.Is(err, ErrUnavailable)`.
    pub fn is_unavailable(&self) -> bool {
        matches!(self, Error::Unavailable(_))
    }

    /// `errors.Is(err, mcp.ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    /// `errors.As(err, &*ToolError)`.
    pub fn as_tool(&self) -> Option<&ToolError> {
        match self {
            Error::Tool(t) => Some(t),
            _ => None,
        }
    }
}

impl From<goap_mcp::Error> for Error {
    fn from(e: goap_mcp::Error) -> Self {
        match e {
            goap_mcp::Error::Invalid(m) => Error::Invalid(m),
        }
    }
}
