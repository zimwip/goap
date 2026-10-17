//! The errors of the package.

/// Error of reading a domain.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The YAML (or JSON) does not decode into a domain, or names a field no definition has: `parse domain: ...`.
    #[error("parse domain: {0}")]
    Parse(String),
    /// A file of [`crate::load_domains`] that does not parse: `<file>: parse domain: ...`.
    #[error("{file}: {source}")]
    File {
        file: String,
        #[source]
        source: Box<Error>,
    },
    #[error("{0}")]
    Io(#[from] std::io::Error),
    /// A domain that does not render (`Domain::yaml`).
    #[error("{0}")]
    Render(String),
}
