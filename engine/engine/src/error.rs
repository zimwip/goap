//! The errors of the engine: Go's sentinels (`ErrNotFound`, `ErrInvalidState`, `ErrFenced`, ...) are variants, the
//! errors of the ports it calls are carried as they are (`errors.Is` becomes an `is_x()` method that looks through
//! the wrappers).

use goap_ctx::CtxError;

/// ErrNotFound's text: unknown processes.
pub const ERR_NOT_FOUND: &str = "process not found";
/// ErrInvalidState's text.
pub const ERR_INVALID_STATE: &str = "invalid process state";
/// ErrFenced's text: a write to a process another replica holds (ADR 0124).
pub const ERR_FENCED: &str = "process held by another engine replica";
/// ErrPlannerAnswer's text.
pub const ERR_PLANNER_ANSWER: &str = "planner: invalid LLM answer";
/// ErrLivePlanner's text.
pub const ERR_LIVE_PLANNER: &str = "planner calls a model live: no preview";
/// ErrNotAProcess's text (it wraps ErrInvalidState).
pub const ERR_NOT_A_PROCESS: &str = "the run does not follow a process: invalid process state";

/// An error of the engine.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Unknown process (`ErrNotFound`): the whole message, `<id>: process not found`.
    #[error("{0}")]
    NotFound(String),
    /// `ErrInvalidState`: the whole message.
    #[error("{0}")]
    InvalidState(String),
    /// `ErrNotAProcess` (wraps `ErrInvalidState`): the whole message.
    #[error("{0}")]
    NotAProcess(String),
    /// `ErrFenced`: the whole message.
    #[error("{0}")]
    Fenced(String),
    /// `ErrUnknownMethodology`: the whole message, `unknown methodology <name>`.
    #[error("{0}")]
    UnknownMethodology(String),
    /// `ErrLivePlanner`.
    #[error("{0}")]
    LivePlanner(String),
    /// `ErrPlannerAnswer`: the whole message.
    #[error("{0}")]
    PlannerAnswer(String),
    /// `fmt.Errorf("<text>: %w", authz.ErrForbidden)`: the whole message.
    #[error("{0}")]
    Forbidden(String),
    /// `errUnknownTrigger`: the whole message, `<key>: unknown trigger`.
    #[error("{0}")]
    UnknownTrigger(String),
    /// `errSkipped`: a trigger that did not fire (loop protection, rate limit).
    #[error("trigger skipped")]
    TriggerSkipped,
    /// A sub-agent waits for a person (`dsl.ErrSuspended`).
    #[error("suspended: waiting for a sub-agent")]
    Suspended,
    #[error(transparent)]
    Authz(#[from] goap_authz::Error),
    #[error(transparent)]
    Graph(#[from] goap_graphapi::Error),
    #[error(transparent)]
    Llm(#[from] goap_llm::Error),
    #[error(transparent)]
    Dsl(#[from] goap_dsl::Error),
    #[error(transparent)]
    Journal(#[from] goap_journal::Error),
    #[error(transparent)]
    Methodology(#[from] goap_methodology::Error),
    #[error(transparent)]
    Condition(#[from] goap_condition::Error),
    #[error(transparent)]
    Domain(#[from] goap_domain::Error),
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    /// A planner's refusal (`goap.ErrNoPlan`).
    #[error(transparent)]
    Goap(#[from] goap_goap::Error),
    #[error(transparent)]
    Ctx(#[from] CtxError),
    /// Any other failure, by its message (Go's `errors.New` / `fmt.Errorf` without `%w`).
    #[error("{0}")]
    Msg(String),
    /// `fmt.Errorf("<context>: %w", err)`: the whole message and the error it wraps.
    #[error("{message}")]
    Wrapped {
        message: String,
        #[source]
        source: Box<Error>,
    },
}

/// The engine's results.
pub type Result<T, E = Error> = std::result::Result<T, E>;

impl Error {
    /// An error with a message.
    pub fn msg(m: impl Into<String>) -> Self {
        Error::Msg(m.into())
    }

    /// `fmt.Errorf("%s: %w", id, ErrNotFound)`.
    pub fn not_found(id: &str) -> Self {
        Error::NotFound(format!("{id}: {ERR_NOT_FOUND}"))
    }

    /// `fmt.Errorf("<text>: %w", ErrInvalidState)`.
    pub fn invalid_state(text: impl std::fmt::Display) -> Self {
        Error::InvalidState(format!("{text}: {ERR_INVALID_STATE}"))
    }

    /// `fmt.Errorf("<text>: %w", authz.ErrForbidden)`.
    pub fn forbidden(text: impl std::fmt::Display) -> Self {
        Error::Forbidden(format!("{text}: {}", goap_authz::ERR_FORBIDDEN))
    }

    /// `fmt.Errorf("<context>: %w", e)`.
    pub fn context(self, prefix: impl std::fmt::Display) -> Self {
        Error::Wrapped {
            message: format!("{prefix}: {self}"),
            source: Box::new(self),
        }
    }

    /// The innermost error the wrappers carry.
    pub fn root(&self) -> &Error {
        match self {
            Error::Wrapped { source, .. } => source.root(),
            e => e,
        }
    }

    /// `errors.Is(err, ErrNotFound)`: an unknown process.
    pub fn is_not_found(&self) -> bool {
        matches!(self.root(), Error::NotFound(_))
    }

    /// `errors.Is(err, ErrInvalidState)` (ErrNotAProcess wraps it).
    pub fn is_invalid_state(&self) -> bool {
        matches!(self.root(), Error::InvalidState(_) | Error::NotAProcess(_))
    }

    /// `errors.Is(err, ErrNotAProcess)`.
    pub fn is_not_a_process(&self) -> bool {
        matches!(self.root(), Error::NotAProcess(_))
    }

    /// `errors.Is(err, ErrFenced)`.
    pub fn is_fenced(&self) -> bool {
        matches!(self.root(), Error::Fenced(_))
    }

    /// `errors.Is(err, ErrUnknownMethodology)`.
    pub fn is_unknown_methodology(&self) -> bool {
        matches!(self.root(), Error::UnknownMethodology(_))
    }

    /// `errors.Is(err, ErrLivePlanner)`.
    pub fn is_live_planner(&self) -> bool {
        matches!(self.root(), Error::LivePlanner(_))
    }

    /// `errors.Is(err, ErrPlannerAnswer)`.
    pub fn is_planner_answer(&self) -> bool {
        matches!(self.root(), Error::PlannerAnswer(_))
    }

    /// `errors.Is(err, goap.ErrNoPlan)`.
    pub fn is_trigger_skipped(&self) -> bool {
        matches!(self.root(), Error::TriggerSkipped)
    }

    pub fn is_unknown_trigger(&self) -> bool {
        matches!(self.root(), Error::UnknownTrigger(_))
    }

    pub fn is_no_plan(&self) -> bool {
        matches!(self.root(), Error::Goap(e) if e.is_no_plan())
    }

    /// `errors.Is(err, authz.ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        match self.root() {
            Error::Forbidden(_) => true,
            Error::Authz(e) => e.is_forbidden(),
            _ => false,
        }
    }

    /// `errors.Is(err, dsl.ErrSuspended)`.
    pub fn is_suspended(&self) -> bool {
        match self.root() {
            Error::Suspended => true,
            Error::Dsl(e) => e.is_suspended(),
            _ => false,
        }
    }

    /// The graph error the wrappers carry, if any.
    pub fn graph(&self) -> Option<&goap_graphapi::Error> {
        match self.root() {
            Error::Graph(e) => Some(e),
            Error::Journal(goap_journal::Error::Log(e)) => Some(e),
            _ => None,
        }
    }

    /// `errors.Is(err, graph.ErrNotFound)`.
    pub fn is_graph_not_found(&self) -> bool {
        self.graph().is_some_and(|e| e.is_not_found())
    }

    /// `errors.Is(err, graph.ErrConflict)`.
    pub fn is_conflict(&self) -> bool {
        self.graph().is_some_and(|e| e.is_conflict())
    }

    /// `errors.Is(err, graph.ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        self.graph().is_some_and(|e| e.is_invalid())
    }

    /// `errors.Is(err, context.Canceled)` (also through a model or script error).
    pub fn is_canceled(&self) -> bool {
        match self.root() {
            Error::Ctx(CtxError::Canceled) => true,
            Error::Llm(e) => e.is_canceled(),
            _ => false,
        }
    }

    /// `errors.Is(err, context.DeadlineExceeded)`.
    pub fn is_deadline_exceeded(&self) -> bool {
        match self.root() {
            Error::Ctx(CtxError::DeadlineExceeded) => true,
            Error::Llm(e) => e.is_deadline_exceeded(),
            _ => false,
        }
    }
}

/// The error a graph port sees for an engine error (the eventing graph, the scripts).
impl From<Error> for goap_dsl::Error {
    fn from(e: Error) -> Self {
        match e {
            Error::Dsl(d) => d,
            e if e.is_suspended() => goap_dsl::Error::Suspended,
            e => goap_dsl::Error::Host(e.to_string()),
        }
    }
}
