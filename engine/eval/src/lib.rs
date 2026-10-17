//! Port of `engine/eval` (see docs/rust-port.md).
//!
//! Package eval is the offline evaluation of LLM work (ADR 0099): cases (a prompt, mocked tools, what is expected)
//! run against a model through the same tool loop as an engine LLM action, scored on their answer, their tool
//! trajectory and, optionally, by a model judging against a rubric. Each case runs several times: the pass rate,
//! tokens and latency are the measure of a prompt or a model change.

mod case;
mod run;

pub use case::*;
pub use run::*;

/// The errors of loading cases.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A case that does not hold, by its message.
    #[error("{0}")]
    Invalid(String),
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Yaml(#[from] serde_yaml_ng::Error),
    /// The error of a file: `<file>: <cause>`.
    #[error("{file}: {source}")]
    File {
        file: String,
        #[source]
        source: Box<Error>,
    },
}
