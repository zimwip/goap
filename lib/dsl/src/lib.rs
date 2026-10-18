//! The scripting DSL (docs/dsl.md).
//!
//! The DSL is the API injected in scripts: script actions (`kind: script`, [`run`]), the algorithms of a domain
//! (validators, transition guards and actions, [`run_algorithm`]) and the adapters of an MCP ([`run_adapter`]). A
//! script reads the blackboard snapshot and the reference domain, buffers writes to the change, and calls the platform
//! (LLM, sub-agents, tools, connector operations) through a host.
//!
//! Scripts are written in JavaScript. The engine that runs them sits behind the [`ScriptEngine`] trait
//! ([`BoaEngine`], on `boa_engine`, by default), so another engine can replace it without touching the scripts: the
//! engine sees the `ctx` object of a usage as a [`ScriptHost`] (methods called with JSON values) and caches what it
//! compiles by the hash of the source ([`ScriptSource`]). The Go language of the scripts (yaegi) is gone: a script
//! declaring `language: go` is refused with [`Error::GoUnsupported`].
//!
//! # Execution model
//!
//! Engines are synchronous; the platform is async. A run ([`run`], [`run_algorithm`], [`run_adapter`]) is an
//! `async fn`: the script runs on a blocking thread (`tokio::task::spawn_blocking`), and every `ctx` method that does
//! I/O (the graph, a model, a tool, a connector) sends its request over a channel to the calling task, which performs
//! it with the run's [`goap_ctx::Ctx`] and sends the answer back while the script thread waits for it. The host is
//! thus borrowed by the run (no `'static` bound) and its futures run on the caller's task. The run's deadline (the
//! job's timeout, the usage's timeout, or the caller's context) sets an [`Interrupt`] the engine checks while the
//! script runs (every few hundred instructions for [`BoaEngine`]); a pending host call is answered with the
//! interruption, and the run returns `script interrupted: context deadline exceeded` as Go did.

mod adapter;
mod algo;
mod boa;
mod bridge;
mod decision;
mod dsl;
mod engine;
mod gofmt;
mod run;
mod values;

pub use adapter::{
    ADAPTER_TIMEOUT, AdapterCtx, AdapterInput, AdapterOutcome, MAX_ADAPTER_CALLS, OperationCall,
    run_adapter,
};
pub use algo::{
    ALGORITHM_TIMEOUT, AlgorithmInput, ChangeInfo, ImpactInfo, LinkInfo, Outcome, TransitionInfo,
    check_algorithm_code, run_algorithm,
};
pub use boa::BoaEngine;
pub use decision::{
    ChangeOption, DecisionPoint, Question, decision_points_from_blackboard, options_from_blackboard,
};
pub use dsl::{
    AgentResult, ChangeImpact, CompleteRequest, CompleteResult, Ctx, Host, Item, Job, Link,
    LinkEnd, LogLine, Node, NodeInto, NodeOp, NodeOpLink, ScriptResult, items_from_blackboard,
};
pub use engine::{
    Interrupt, ScriptEngine, ScriptError, ScriptHost, ScriptSource, engine, set_engine,
};
pub use gofmt::sprint;
pub use run::DEFAULT_TIMEOUT;
pub use run::run;

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrSuspended is returned by [`Host::run_agent`] when the sub-agent is blocked (waiting for a human): the
    /// action is suspended and retried later.
    #[error("suspended: waiting for a sub-agent")]
    Suspended,
    /// A script language the DSL does not run.
    #[error("unsupported language {0:?}")]
    UnsupportedLanguage(String),
    /// The Go language of the scripts (yaegi) left the DSL with the port.
    #[error("the go script language is no longer supported: rewrite the script in javascript")]
    GoUnsupported,
    /// The script failed: it does not compile, it threw, it exceeded a limit of the engine.
    #[error("{0}")]
    Script(String),
    /// The run was interrupted (its deadline passed or its context was cancelled): `script interrupted: <cause>`.
    #[error("script interrupted: {0}")]
    Interrupted(String),
    /// A request that does not hold (an algorithm of a type that cannot be run, a bad argument).
    #[error("{0}")]
    Invalid(String),
    /// The failure of an algorithm's script: `algorithm <instance>: <cause>`.
    #[error("algorithm {instance}: {source}")]
    Algorithm {
        instance: String,
        #[source]
        source: Box<Error>,
    },
    /// The failure of an adapter's script: `adapter <instance>: <cause>`.
    #[error("adapter {instance}: {source}")]
    Adapter {
        instance: String,
        #[source]
        source: Box<Error>,
    },
    /// A failure of the host (the graph, a model, a tool, a connector), by its message.
    #[error("{0}")]
    Host(String),
    /// Any other failure of the host.
    #[error(transparent)]
    Other(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// A failure of the host with a message (Go's `errors.New` / `fmt.Errorf`).
    pub fn host(msg: impl Into<String>) -> Self {
        Error::Host(msg.into())
    }

    /// Wraps any error.
    pub fn other(e: impl std::error::Error + Send + Sync + 'static) -> Self {
        Error::Other(Box::new(e))
    }

    /// `errors.Is(err, ErrSuspended)`, through the algorithm / adapter wrappers.
    pub fn is_suspended(&self) -> bool {
        match self {
            Error::Suspended => true,
            Error::Algorithm { source, .. } | Error::Adapter { source, .. } => {
                source.is_suspended()
            }
            _ => false,
        }
    }

    /// Whether the run was interrupted (deadline or cancellation), through the wrappers.
    pub fn is_interrupted(&self) -> bool {
        match self {
            Error::Interrupted(_) => true,
            Error::Algorithm { source, .. } | Error::Adapter { source, .. } => {
                source.is_interrupted()
            }
            _ => false,
        }
    }
}
