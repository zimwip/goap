//! Port of `engine/sandbox` (see docs/rust-port.md).
//!
//! Package sandbox runs script actions outside of the engine: a [`Provisioner`] starts one sandbox per process run (a
//! process on bare metal, a container with Docker, a pod on Kubernetes) running goap-runner; the engine sends it jobs
//! and serves the DSL operations back through RuntimeService ([`Runtime`]).
//!
//! Scripts are JavaScript only (the Go scripts left the DSL with the port).

mod config;
mod docker;
mod kubernetes;
mod pool;
mod process;
mod runner;
mod runtime;

pub use config::{FromEnv, from_env};
pub use docker::DockerProvisioner;
pub use kubernetes::KubernetesProvisioner;
pub use pool::{Instance, Pool, Provisioner, Spec};
pub use process::ProcessProvisioner;
pub use runner::Runner;
pub use runtime::{RemoteHost, Runtime, Unregister};

/// The errors of the provisioners and of the pool.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A failure by its message (Go's `errors.New` / `fmt.Errorf`).
    #[error("{0}")]
    Msg(String),
    /// An API answered with an error status: the whole message, and the status.
    #[error("{message}")]
    Status { code: u16, message: String },
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Http(#[from] reqwest::Error),
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    #[error(transparent)]
    Ctx(#[from] goap_ctx::CtxError),
}

impl Error {
    pub fn msg(m: impl Into<String>) -> Self {
        Error::Msg(m.into())
    }
}

/// Runs `f` until it ends or `ctx` is done.
pub(crate) async fn with_ctx<T, E>(
    ctx: &goap_ctx::Ctx,
    f: impl Future<Output = Result<T, E>>,
) -> Result<T, Error>
where
    Error: From<E>,
{
    tokio::select! {
        r = f => r.map_err(Error::from),
        e = ctx.done() => Err(Error::Ctx(e)),
    }
}

/// Go's `url.PathEscape` / `url.QueryEscape` for the names a provisioner puts in a URL.
pub(crate) fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

#[cfg(test)]
mod tests;
