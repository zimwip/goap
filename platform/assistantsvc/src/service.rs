//! The service: its errors, limits and ports, Send and Cancel (`service.go`).

use std::collections::HashMap;
use std::error::Error as StdError;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, LazyLock};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_authz::{Principal, principal_from, with_principal};
use goap_convsvc as convsvc;
use goap_ctx::{CancelHandle, Ctx};
use goap_domain::{Change, ChangeID};
use goap_graphapi::{ChangesFilter, NewChange};
use goap_llm as llm;
use goap_methodology::Compiled;
use parking_lot::Mutex;

use crate::compact::compactable;
use crate::context::Context;
use crate::engine::Engine;
use crate::frame::searched_in;
use crate::turn::Turn;
use crate::uitools::{UITool, check_tools, tool_index};
use crate::util::{clip, q};

/// Any error of a port (the organisation, the registry, the engine).
pub type BoxError = Box<dyn StdError + Send + Sync>;

/// The errors of the Service; the errors of the conversation service pass through ([`Error::Conv`]).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrInvalid: a request the service does not accept (malformed, or beyond a limit): `invalid: <detail>`.
    #[error("invalid: {0}")]
    Invalid(String),
    /// ErrForbidden: the caller may not do that (a project they cannot work on); the detail is the error joined to it.
    #[error("{}", if .0.is_empty() { "forbidden".to_string() } else { format!("forbidden\n{}", .0) })]
    Forbidden(String),
    /// ErrUnavailable: the `assistant` model alias cannot be used by the caller.
    #[error("the assistant is not available: {0}")]
    Unavailable(String),
    /// ErrBusy: the assistant is still answering the previous message of the conversation.
    #[error("the assistant is still answering the previous message")]
    Busy,
    /// ErrDecided: the action was already decided.
    #[error("this action was already decided")]
    Decided,
    /// ErrStale: a proposal no longer holds; it stays proposed.
    #[error("the proposal is stale: {0}")]
    Stale(String),
    /// ErrState: the action is not in a state that allows the operation.
    #[error("{}", if .0.is_empty() { "the action is not in a state that allows this".to_string() } else { format!("the action is not in a state that allows this: {}", .0) })]
    State(String),
    /// errStopped: the person stopped the answer (Cancel).
    #[error("the person stopped the answer")]
    Stopped,
    /// An error of the conversation service.
    #[error(transparent)]
    Conv(convsvc::Error),
    /// An error of the model gateway.
    #[error(transparent)]
    Model(#[from] goap_modelgw::Error),
    /// Any other failure, its message as Go writes it.
    #[error("{0}")]
    Message(String),
}

impl From<convsvc::Error> for Error {
    /// An error of the service carried through the conversation service (the function of an update) comes back as
    /// itself.
    fn from(e: convsvc::Error) -> Self {
        match e {
            convsvc::Error::Other(b) => match b.downcast::<Error>() {
                Ok(e) => *e,
                Err(b) => Error::Conv(convsvc::Error::Other(b)),
            },
            e => Error::Conv(e),
        }
    }
}

impl Error {
    /// `errors.Is(err, ErrInvalid)` (this service's or the conversation service's).
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_)) || matches!(self, Error::Conv(e) if e.is_invalid())
    }
    /// `errors.Is(err, ErrForbidden)` (this service's or the conversation service's).
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Forbidden(_)) || matches!(self, Error::Conv(e) if e.is_forbidden())
    }
    /// `errors.Is(err, convsvc.ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::Conv(e) if e.is_not_found())
    }
    /// `errors.Is(err, convsvc.ErrAnonymous)`.
    pub fn is_anonymous(&self) -> bool {
        matches!(self, Error::Conv(e) if e.is_anonymous())
    }
    pub fn is_unavailable(&self) -> bool {
        matches!(self, Error::Unavailable(_))
    }
    pub fn is_busy(&self) -> bool {
        matches!(self, Error::Busy)
    }
    pub fn is_decided(&self) -> bool {
        matches!(self, Error::Decided)
    }
    pub fn is_stale(&self) -> bool {
        matches!(self, Error::Stale(_))
    }
    pub fn is_state(&self) -> bool {
        matches!(self, Error::State(_))
    }
    pub fn is_stopped(&self) -> bool {
        matches!(self, Error::Stopped)
    }
}

/// Alias is the model alias that serves the assistant (a protected alias, ADR 0084).
pub const ALIAS: &str = goap_llmcfg::ASSISTANT_ALIAS;

/// MaxRounds is the number of model calls of a turn: each may ask for tools, whose results go back to the model in
/// the next call; the last one is offered no tool and must answer with what it has. The person can stop a turn at any
/// time (Cancel).
pub const MAX_ROUNDS: usize = 16;
/// MaxToolCalls caps the tools run in one round; the others get an error as their result.
pub const MAX_TOOL_CALLS: usize = 4;
/// MaxTextBytes caps a user message.
pub const MAX_TEXT_BYTES: usize = 4 << 10;
/// MaxContextBytes caps the context snapshot of a request once rendered (context.rs), MaxSelectionBytes the
/// selected text.
pub const MAX_CONTEXT_BYTES: usize = 8 << 10;
pub const MAX_SELECTION_BYTES: usize = 2000;
/// MaxHistoryMessages and MaxHistoryBytes cap what is sent of the conversation: its last messages, newest first,
/// while they fit.
pub const MAX_HISTORY_MESSAGES: usize = 12;
pub const MAX_HISTORY_BYTES: usize = 16 << 10;
/// TurnTimeout bounds one turn; a pending message older than StaleAfter belongs to a turn that died with its process
/// and no longer blocks the conversation.
pub const TURN_TIMEOUT: Duration = Duration::from_secs(5 * 60);
pub const STALE_AFTER: Duration = Duration::from_secs(7 * 60);

pub(crate) const MAX_ANSWER_BYTES: usize = 8 << 10;
pub(crate) const MAX_TOOL_RESULT: usize = 8 << 10;
pub(crate) const MAX_METHODOLOGIES: usize = 30;
pub(crate) const MAX_ERROR_TEXT: usize = 500;

/// Model is what the assistant needs of the gateway: the model call under the catalog policy and the aliases the
/// caller may use (`goap_modelgw::Service`, `goap_modelgw::Client`).
pub use goap_modelgw::SuggestModel as Model;

/// Graph is what the assistant needs of the graph: to create a change, to read one and to list them, as the caller.
#[async_trait]
pub trait Graph: Send + Sync {
    async fn create_change(
        &self,
        ctx: &Ctx,
        in_: NewChange,
    ) -> Result<Change, goap_graphapi::Error>;
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, goap_graphapi::Error>;
    async fn list_changes(
        &self,
        ctx: &Ctx,
        f: ChangesFilter,
    ) -> Result<Vec<Change>, goap_graphapi::Error>;
    /// AttachChange and DetachChange carry out the attach / detach proposals the person accepted (ADR 0106).
    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, goap_graphapi::Error>;
    async fn detach_change(&self, ctx: &Ctx, id: &ChangeID)
    -> Result<Change, goap_graphapi::Error>;
}

/// PortGraph is the Graph of any graph port (the graph itself, its client, the eventing graph of the engine).
pub struct PortGraph(pub Arc<dyn goap_graphapi::Port>);

#[async_trait]
impl Graph for PortGraph {
    async fn create_change(
        &self,
        ctx: &Ctx,
        in_: NewChange,
    ) -> Result<Change, goap_graphapi::Error> {
        self.0.create_change(ctx, in_).await
    }
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, goap_graphapi::Error> {
        self.0.change(ctx, id).await
    }
    async fn list_changes(
        &self,
        ctx: &Ctx,
        f: ChangesFilter,
    ) -> Result<Vec<Change>, goap_graphapi::Error> {
        self.0.list_changes(ctx, f).await
    }
    async fn attach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        parent: &ChangeID,
    ) -> Result<Change, goap_graphapi::Error> {
        self.0.attach_change(ctx, id, parent).await
    }
    async fn detach_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
    ) -> Result<Change, goap_graphapi::Error> {
        self.0.detach_change(ctx, id).await
    }
}

/// Methodologies serves a methodology by name (the registry service or its client).
#[async_trait]
pub trait Methodologies: Send + Sync {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> Result<Arc<Compiled>, BoxError>;
}

/// Projects is what the assistant asks of the organisation: which projects exist, whether the caller may work on one
/// and which methodologies apply to it ([`Directory`]).
#[async_trait]
pub trait Projects: Send + Sync {
    async fn has_project(&self, ctx: &Ctx, project: &str) -> Result<bool, BoxError>;
    /// RootProject is the project of a caller with no active project (ADR 0091).
    async fn root_project(&self, ctx: &Ctx) -> Result<String, BoxError>;
    async fn may_access_project(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, BoxError>;
    async fn applicable_methodologies(
        &self,
        ctx: &Ctx,
        project: &str,
    ) -> Result<Vec<String>, BoxError>;
    /// SubProjects is what a Projects may also tell: a project and the projects below it (Directory does). None
    /// (the default): the search covers the active project only.
    async fn sub_projects(
        &self,
        _ctx: &Ctx,
        _project: &str,
    ) -> Option<Result<Vec<String>, BoxError>> {
        None
    }
}

/// Directory answers Projects from the snapshot of the organisation the access directory keeps.
pub struct Directory(pub Arc<goap_access::Directory>);

fn unreadable(e: impl std::fmt::Display) -> BoxError {
    format!("the organisation cannot be read: {e}").into()
}

#[async_trait]
impl Projects for Directory {
    async fn root_project(&self, ctx: &Ctx) -> Result<String, BoxError> {
        match self.0.snapshot(ctx).await {
            Ok(s) => Ok(s.root_project()),
            Err(e) => Err(unreadable(e.error)),
        }
    }

    async fn has_project(&self, ctx: &Ctx, project: &str) -> Result<bool, BoxError> {
        let s = self
            .0
            .snapshot(ctx)
            .await
            .map_err(|e| BoxError::from(e.error.to_string()))?;
        Ok(s.has_project(project))
    }

    async fn may_access_project(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, BoxError> {
        self.0
            .may_access_project(ctx, p, project)
            .await
            .map_err(|e| BoxError::from(e.error.to_string()))
    }

    async fn applicable_methodologies(
        &self,
        ctx: &Ctx,
        project: &str,
    ) -> Result<Vec<String>, BoxError> {
        let s = self
            .0
            .snapshot(ctx)
            .await
            .map_err(|e| BoxError::from(e.error.to_string()))?;
        Ok(s.applicable_methodologies(project))
    }

    async fn sub_projects(
        &self,
        ctx: &Ctx,
        project: &str,
    ) -> Option<Result<Vec<String>, BoxError>> {
        Some(
            self.0
                .sub_projects(ctx, project)
                .await
                .map_err(|e| BoxError::from(e.error.to_string())),
        )
    }
}

/// The clock of a [`Service`].
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// A turn to run in the background.
pub type TurnFuture = Pin<Box<dyn Future<Output = ()> + Send>>;

/// Runner starts the background turn (Go's `Service.Go`).
#[derive(Clone, Default)]
pub enum Runner {
    /// A task of its own (the default).
    #[default]
    Spawn,
    /// Inline, before Send returns (tests).
    Inline,
    /// Handed to a function (tests that run it later).
    With(Arc<dyn Fn(TurnFuture) + Send + Sync>),
}

/// Service answers the messages of the conversations.
///
/// A turn is the pending assistant message itself: Send appends the user message and a pending assistant message,
/// and returns; the turn then runs in the background as the caller (model call, tools) and writes the answer into
/// that message as the platform service `system:assistant`. There is no engine process for it (the engine's llm
/// action is a one-shot protocol and cannot chat): the message, which names its conversation, is the record of the
/// run.
pub struct Service {
    pub convs: Arc<convsvc::Service>,
    pub model: Arc<dyn Model>,
    pub graph: Arc<dyn Graph>,
    pub methodologies: Arc<dyn Methodologies>,
    pub projects: Arc<dyn Projects>,
    /// Engine runs the agents the person confirms (ADR 0090); None: the agent tools refuse.
    pub engine: Option<Arc<dyn Engine>>,
    /// Now is the clock (Utc::now when None); go starts the background turn.
    pub now: Option<Clock>,
    pub go: Runner,

    /// conversation ids with a turn in this process
    running: Mutex<HashMap<String, LiveTurn>>,
}

/// liveTurn is a turn of this process: its context is cancelled when the person stops it.
struct LiveTurn {
    /// None until the turn has started
    cancel: Option<CancelHandle>,
    /// the person stopped it (before it started, or while it runs): the cause of its cancellation
    stopped: Arc<AtomicBool>,
}

/// Principal is the identity under which the answers are written (ADR 0085: only a platform service writes the words
/// of the assistant).
pub static PRINCIPAL: LazyLock<Principal> = LazyLock::new(|| goap_authz::system("assistant", &[]));

/// SendInput is a user message with the context of the page it was written on.
#[derive(Debug, Clone, Default)]
pub struct SendInput {
    pub conversation_id: String,
    pub text: String,
    pub context: Context,
    /// UITools are the tools the current screen offers this turn (ADR 0092); the service never runs them.
    pub ui_tools: Vec<UITool>,
}

/// Releases the busy mark of a conversation unless the turn started.
struct Release<'a> {
    s: &'a Service,
    id: String,
    armed: bool,
}

impl Drop for Release<'_> {
    fn drop(&mut self) {
        if self.armed {
            self.s.release(&self.id);
        }
    }
}

impl Service {
    /// A service over its ports, with no engine, the system clock and turns in tasks of their own.
    pub fn new(
        convs: Arc<convsvc::Service>,
        model: Arc<dyn Model>,
        graph: Arc<dyn Graph>,
        methodologies: Arc<dyn Methodologies>,
        projects: Arc<dyn Projects>,
    ) -> Service {
        Service {
            convs,
            model,
            graph,
            methodologies,
            projects,
            engine: None,
            now: None,
            go: Runner::Spawn,
            running: Mutex::new(HashMap::new()),
        }
    }

    pub(crate) fn now(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    /// timed logs, at debug level, the duration of a phase of the assistant: the measure of where a turn spends its
    /// time.
    pub(crate) fn timed(&self, phase: &str, start: Instant) {
        tracing::debug!(
            phase,
            duration_ms = start.elapsed().as_micros() as f64 / 1000.0,
            "assistant phase"
        );
    }

    fn acquire(&self, id: &str) -> bool {
        let mut r = self.running.lock();
        if r.contains_key(id) {
            return false;
        }
        r.insert(
            id.to_string(),
            LiveTurn {
                cancel: None,
                stopped: Arc::new(AtomicBool::new(false)),
            },
        );
        true
    }

    /// started records the cancel handle of the turn of a conversation and returns its stop flag; a turn stopped
    /// before it started is cancelled at once.
    fn started(&self, id: &str, cancel: CancelHandle) -> Arc<AtomicBool> {
        let mut r = self.running.lock();
        match r.get_mut(id) {
            Some(t) => {
                if t.stopped.load(Ordering::SeqCst) {
                    cancel.cancel();
                }
                t.cancel = Some(cancel);
                t.stopped.clone()
            }
            None => Arc::new(AtomicBool::new(false)),
        }
    }

    /// stop cancels the turn of a conversation running in this process; it reports whether there was one.
    fn stop(&self, id: &str) -> bool {
        let r = self.running.lock();
        let Some(t) = r.get(id) else {
            return false;
        };
        t.stopped.store(true, Ordering::SeqCst);
        if let Some(c) = &t.cancel {
            c.cancel();
        }
        true
    }

    fn release(&self, id: &str) {
        self.running.lock().remove(id);
    }

    /// Send appends the user message of the caller to their conversation and a pending assistant message, starts the
    /// turn and returns both messages at once. It is refused when the caller has no `assistant` model alias
    /// (Unavailable), when the previous message is still being answered (Busy) and when the active project is one
    /// the caller may not work on (Forbidden).
    pub async fn send(
        self: &Arc<Self>,
        ctx: &Ctx,
        mut in_: SendInput,
    ) -> Result<(convsvc::Message, convsvc::Message), Error> {
        let t_send = Instant::now();
        let mut p = principal_from(ctx);
        if p.anonymous() {
            return Err(convsvc::Error::Anonymous.into());
        }
        if p.is_system() {
            return Err(convsvc::Error::Forbidden.into());
        }
        if in_.text.trim().is_empty() || in_.text.len() > MAX_TEXT_BYTES {
            return Err(Error::Invalid(format!(
                "a message needs a text of at most {MAX_TEXT_BYTES} bytes"
            )));
        }
        in_.context = in_.context.normalise()?;
        in_.ui_tools = check_tools(&in_.ui_tools)?;
        let t0 = Instant::now();
        let (_, aliases) = self.model.available(ctx).await?;
        self.timed("send.available", t0);
        if !aliases.iter().any(|a| a.alias == ALIAS) {
            return Err(Error::Unavailable(format!(
                "the {} model alias is not available to you",
                q(ALIAS)
            )));
        }
        let project = in_.context.app.project.clone();
        if !project.is_empty() && project != p.project {
            match self.projects.may_access_project(ctx, &p, &project).await {
                Ok(true) => {}
                Ok(false) => return Err(Error::Forbidden(String::new())),
                Err(e) => return Err(Error::Forbidden(e.to_string())),
            }
        }
        if !self.acquire(&in_.conversation_id) {
            return Err(Error::Busy);
        }
        let mut guard = Release {
            s: self,
            id: in_.conversation_id.clone(),
            armed: true,
        };

        let t0 = Instant::now();
        let (_, msgs) = self.convs.get(ctx, &p, &in_.conversation_id).await?;
        self.timed("send.history", t0);
        if let Some(last) = msgs.last()
            && last.role == convsvc::ROLE_ASSISTANT
            && last.status == convsvc::STATUS_PENDING
            && self.now() - last.created_at
                < chrono::TimeDelta::from_std(STALE_AFTER).unwrap_or_default()
        {
            return Err(Error::Busy);
        }
        let (history, from) = history_of(&msgs);
        let summary = self
            .convs
            .summary(ctx, &PRINCIPAL, &in_.conversation_id)
            .await?;
        let user = self
            .convs
            .append(
                ctx,
                &p,
                &in_.conversation_id,
                convsvc::ROLE_USER,
                convsvc::Content {
                    text: in_.text.clone(),
                    context: in_.context.describe(),
                    ..Default::default()
                },
            )
            .await?;
        let pending = self
            .convs
            .append(
                ctx,
                &PRINCIPAL,
                &in_.conversation_id,
                convsvc::ROLE_ASSISTANT,
                convsvc::Content {
                    status: convsvc::STATUS_PENDING.into(),
                    ..Default::default()
                },
            )
            .await?;
        // the turn runs as the caller, whose context ends with the request: it gets its own, with the same values
        if !in_.context.app.project.is_empty() {
            p.project = in_.context.app.project.clone();
        }
        let older = compactable(&msgs[..from], summary.up_to);
        let searched = searched_in(&msgs);
        let conversation = in_.conversation_id.clone();
        let mut t = Turn::new(
            self.clone(),
            p.clone(),
            conversation.clone(),
            pending.id.clone(),
            tool_index(&in_.ui_tools),
            in_,
            history,
            summary,
            older,
            searched,
        );
        let s = self.clone();
        let base = ctx.clone();
        let run = async move {
            let tctx = with_principal(&base.without_cancel(), p);
            let (rctx, cancel) = tctx.with_cancel();
            let stopped = s.started(&conversation, cancel.clone());
            t.stopped = stopped;
            t.run(&rctx).await;
            cancel.cancel();
            s.release(&conversation); // the next message need not wait for the summary
            t.compact(&tctx).await;
        };
        guard.armed = false;
        drop(guard);
        self.timed("send", t_send);
        match &self.go {
            Runner::Spawn => {
                tokio::spawn(run);
            }
            Runner::Inline => run.await,
            Runner::With(f) => f(Box::pin(run)),
        }
        Ok((user, pending))
    }

    /// Cancel stops the answer being written in a conversation of the caller: the pending assistant message becomes
    /// cancelled at once (with what it holds) and the turn, in this process or in another one (it reads the message
    /// between its rounds), stops before its next model call; tools already run stay done, proposals already made
    /// stay to decide. With no answer pending it returns the last message as it is.
    pub async fn cancel(
        &self,
        ctx: &Ctx,
        conversation_id: &str,
    ) -> Result<convsvc::Message, Error> {
        let p = principal_from(ctx);
        if p.anonymous() {
            return Err(convsvc::Error::Anonymous.into());
        }
        if p.is_system() {
            return Err(convsvc::Error::Forbidden.into());
        }
        let (_, msgs) = self.convs.get(ctx, &p, conversation_id).await?; // the owner only
        let Some(last) = msgs.last() else {
            return Err(Error::Invalid("the conversation has no message".into()));
        };
        if last.role != convsvc::ROLE_ASSISTANT || last.status != convsvc::STATUS_PENDING {
            return Ok(last.clone());
        }
        let wctx = with_principal(&ctx.without_cancel(), PRINCIPAL.clone());
        let m = match self
            .convs
            .update_message(
                &wctx,
                &PRINCIPAL,
                &last.id,
                convsvc::Content {
                    text: last.text.clone(),
                    actions: last.actions.clone(),
                    status: convsvc::STATUS_CANCELLED.into(),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(m) => m,
            Err(e) if e.is_cancelled() => convsvc::Message::default(),
            Err(e) => return Err(e.into()),
        };
        self.stop(conversation_id);
        Ok(m)
    }
}

/// historyOf is what is sent of the conversation before the new message: its last messages that fit the caps,
/// oldest first, the pending and the empty ones left out. from is the index in msgs of the oldest message the window
/// holds (msgs.len() when none): the ones before are the summary's (compact.rs).
pub(crate) fn history_of(msgs: &[convsvc::Message]) -> (Vec<llm::Message>, usize) {
    let mut size = 0;
    let mut out: Vec<llm::Message> = Vec::new();
    let mut at: Vec<usize> = Vec::new(); // the index in msgs of each message of out
    let mut i = msgs.len();
    while i > 0 && out.len() < MAX_HISTORY_MESSAGES - 1 {
        i -= 1;
        let m = &msgs[i];
        let mut text = m.text.trim().to_string();
        if m.status == convsvc::STATUS_CANCELLED && text.is_empty() {
            text = "[the person stopped this answer]".into();
        }
        if m.status == convsvc::STATUS_PENDING || text.is_empty() {
            continue;
        }
        text = clip(&text, MAX_TEXT_BYTES);
        if m.role == convsvc::ROLE_ASSISTANT {
            let acts = crate::agents::describe_actions(&m.actions);
            if !acts.is_empty() {
                text.push('\n');
                text.push_str(&acts);
            }
        }
        size += text.len();
        if size > MAX_HISTORY_BYTES {
            break;
        }
        out.push(llm::Message {
            role: m.role.clone(),
            content: text,
            ..Default::default()
        });
        at.push(i);
    }
    // the window starts with a person's message (providers want a user turn first): an assistant message at its
    // start is left to the summary rather than dropped
    while out.last().is_some_and(|m| m.role != convsvc::ROLE_USER) {
        out.pop();
        at.pop();
    }
    let from = at.last().copied().unwrap_or(msgs.len());
    out.reverse();
    (out, from)
}
