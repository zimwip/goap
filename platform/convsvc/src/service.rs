use std::{error::Error as StdError, sync::Arc};

use chrono::{DateTime, Utc};
use goap_authz::Principal;
use goap_ctx::Ctx;

use crate::store::{
    Action, ActionFn, Conversation, Cursor, Message, ROLE_ASSISTANT, ROLE_USER, STATUS_CANCELLED,
    STATUS_DONE, STATUS_ERROR, STATUS_PENDING, Store, Summary, from_micros, go_json, micros,
};

/// The errors of the service and its stores (Go's sentinels).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A request the service does not accept, malformed or beyond a limit (`ErrInvalid`): the whole message,
    /// `invalid: ...`.
    #[error("{0}")]
    Invalid(String),
    /// The caller is not identified (`ErrAnonymous`): conversations belong to a declared user.
    #[error("conversations need an identified caller")]
    Anonymous,
    /// The caller may not do that on a conversation they can see (`ErrForbidden`).
    #[error("forbidden")]
    Forbidden,
    /// An assistant message the person stopped would be given another status (`ErrCancelled`, an `ErrInvalid`).
    #[error("invalid: the answer was stopped")]
    Cancelled,
    /// The message has no action at that index (`ErrActionMissing`, an `ErrInvalid`).
    #[error("invalid: no such action")]
    ActionMissing,
    /// `ErrNotFound` of a store.
    #[error("not found")]
    NotFound,
    /// A limit given to the Store would be exceeded (`ErrFull`).
    #[error("limit reached")]
    Full,
    /// Stored actions that do not decode.
    #[error("stored actions: {0}")]
    StoredActions(#[source] serde_json::Error),
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    /// Any other error, such as the one of the function given to [`Service::update_action`].
    #[error(transparent)]
    Other(Box<dyn StdError + Send + Sync>),
}

impl Error {
    /// An error of the caller's, carried as is (`downcast_other` finds it again).
    pub fn other(e: impl StdError + Send + Sync + 'static) -> Self {
        Error::Other(Box::new(e))
    }

    /// The error carried by [`Error::Other`], as an `E`.
    pub fn downcast_other<E: StdError + 'static>(&self) -> Option<&E> {
        match self {
            Error::Other(e) => e.downcast_ref(),
            _ => None,
        }
    }

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(
            self,
            Error::Invalid(_) | Error::Cancelled | Error::ActionMissing
        )
    }
    pub fn is_anonymous(&self) -> bool {
        matches!(self, Error::Anonymous)
    }
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Forbidden)
    }
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound)
    }
    pub fn is_full(&self) -> bool {
        matches!(self, Error::Full)
    }
    pub fn is_cancelled(&self) -> bool {
        matches!(self, Error::Cancelled)
    }
    pub fn is_action_missing(&self) -> bool {
        matches!(self, Error::ActionMissing)
    }
}

fn invalid(msg: impl std::fmt::Display) -> Error {
    Error::Invalid(format!("invalid: {msg}"))
}

// Limits: they protect the storage of the service, not the use of the assistant.
pub const MAX_CONVERSATIONS_PER_SUBJECT: i64 = 200;
pub const MAX_MESSAGES_PER_CONVERSATION: i64 = 500;
pub const MAX_TEXT_BYTES: usize = 64 << 10;
pub const MAX_ACTIONS_BYTES: usize = 64 << 10;
pub const MAX_TITLE_BYTES: usize = 200;
/// MaxSummaryBytes caps the rolling summary of a conversation.
pub const MAX_SUMMARY_BYTES: usize = 8 << 10;
pub const MAX_ERROR_BYTES: usize = 4 << 10;
/// MaxContextBytes caps the description of the context of a user message.
pub const MAX_CONTEXT_BYTES: usize = 300;
pub const DEFAULT_PAGE_SIZE: i64 = 50;
pub const MAX_PAGE_SIZE: i64 = 200;
/// DefaultTitle names a conversation created without a title.
pub const DEFAULT_TITLE: &str = "New conversation";

/// The clock of a [`Service`].
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;
/// The id maker of a [`Service`].
pub type NewId = Arc<dyn Fn() -> String + Send + Sync>;

/// Service reads and writes the conversations of the callers.
///
/// Who may do what: the owner of a conversation lists, reads, renames, deletes it and appends user messages to it. A
/// platform service (a system principal, the executor of the assistant) appends assistant messages to any
/// conversation and updates them, but reads no conversation and lists none. A conversation of someone else answers
/// as if it did not exist.
#[derive(Clone)]
pub struct Service {
    pub store: Arc<dyn Store>,
    /// The clock (`Utc::now` when None); new_id makes the ids (random when None).
    pub now: Option<Clock>,
    pub new_id: Option<NewId>,
}

/// Content is what a message says.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Content {
    pub text: String,
    pub actions: Vec<Action>,
    pub process_id: String,
    pub status: String,
    pub error: String,
    /// Context describes, in a few words, what the user was looking at (user messages only).
    pub context: String,
}

impl Content {
    fn check(&self) -> Result<(), Error> {
        if self.text.len() > MAX_TEXT_BYTES {
            return Err(invalid(format_args!(
                "the text is {} bytes, at most {MAX_TEXT_BYTES}",
                self.text.len()
            )));
        }
        if self.context.len() > MAX_CONTEXT_BYTES {
            return Err(invalid(format_args!(
                "the context is {} bytes, at most {MAX_CONTEXT_BYTES}",
                self.context.len()
            )));
        }
        if self.error.len() > MAX_ERROR_BYTES {
            return Err(invalid(format_args!(
                "the error is {} bytes, at most {MAX_ERROR_BYTES}",
                self.error.len()
            )));
        }
        match self.status.as_str() {
            STATUS_PENDING | STATUS_DONE | STATUS_ERROR | STATUS_CANCELLED => {}
            s => {
                return Err(invalid(format_args!(
                    "status {} is not pending, done, error or cancelled",
                    quote(s)
                )));
            }
        }
        for (i, a) in self.actions.iter().enumerate() {
            if a.get("type")
                .and_then(|t| t.as_str())
                .unwrap_or("")
                .is_empty()
            {
                return Err(invalid(format_args!("action {i} has no type")));
            }
        }
        if !self.actions.is_empty() {
            let n = go_json(&self.actions).len();
            if n > MAX_ACTIONS_BYTES {
                return Err(invalid(format_args!(
                    "the actions are {n} bytes, at most {MAX_ACTIONS_BYTES}"
                )));
            }
        }
        Ok(())
    }
}

/// Go's `%q` of a string.
fn quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

fn check_title(t: &str) -> Result<String, Error> {
    let t = t.trim();
    if t.len() > MAX_TITLE_BYTES {
        return Err(invalid(format_args!(
            "the title is {} bytes, at most {MAX_TITLE_BYTES}",
            t.len()
        )));
    }
    Ok(t.to_string())
}

fn store_err(err: Error) -> Error {
    match err {
        Error::Full => invalid(Error::Full),
        e => e,
    }
}

fn parse_token(t: &str) -> Result<Cursor, Error> {
    if t.is_empty() {
        return Ok(Cursor::default());
    }
    let bad = || invalid("page token");
    let (ts, id) = t.split_once('.').ok_or_else(bad)?;
    let n: i64 = ts.parse().map_err(|_| bad())?;
    if id.is_empty() {
        return Err(bad());
    }
    Ok(Cursor {
        updated_at: Some(from_micros(n)),
        id: id.to_string(),
    })
}

impl Service {
    pub fn new(store: Arc<dyn Store>) -> Self {
        Service {
            store,
            now: None,
            new_id: None,
        }
    }

    fn now(&self) -> DateTime<Utc> {
        let t = match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        };
        from_micros(micros(&t))
    }

    fn new_id(&self, prefix: &str) -> String {
        if let Some(f) = &self.new_id {
            return f();
        }
        let b = uuid::Uuid::new_v4();
        let hex: String = b.as_bytes()[..12]
            .iter()
            .map(|x| format!("{x:02x}"))
            .collect();
        format!("{prefix}{hex}")
    }

    /// owned returns a conversation of the caller; one of someone else (or none) is not found.
    async fn owned(&self, ctx: &Ctx, p: &Principal, id: &str) -> Result<Conversation, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if p.is_system() || id.is_empty() {
            return Err(Error::NotFound);
        }
        let c = self.store.get(ctx, id).await?;
        if c.subject != p.subject {
            return Err(Error::NotFound);
        }
        Ok(c)
    }

    /// Create opens an empty conversation owned by the caller (a person: a platform service has none).
    pub async fn create(
        &self,
        ctx: &Ctx,
        p: &Principal,
        title: &str,
    ) -> Result<Conversation, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if p.is_system() {
            return Err(Error::Forbidden);
        }
        let mut title = check_title(title)?;
        if title.is_empty() {
            title = DEFAULT_TITLE.to_string();
        }
        let now = self.now();
        let c = Conversation {
            id: self.new_id("CONV-"),
            subject: p.subject.clone(),
            title,
            created_at: now,
            updated_at: now,
        };
        self.store
            .create(ctx, c.clone(), MAX_CONVERSATIONS_PER_SUBJECT)
            .await
            .map_err(store_err)?;
        Ok(c)
    }

    /// List returns a page of the conversations of the caller, most recently updated first, and the token of the
    /// next page ("" on the last one).
    pub async fn list(
        &self,
        ctx: &Ctx,
        p: &Principal,
        page_size: i64,
        token: &str,
    ) -> Result<(Vec<Conversation>, String), Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if p.is_system() {
            return Err(Error::Forbidden);
        }
        let page_size = if page_size <= 0 {
            DEFAULT_PAGE_SIZE
        } else {
            page_size
        }
        .min(MAX_PAGE_SIZE);
        let after = parse_token(token)?;
        let mut out = self
            .store
            .list(ctx, &p.subject, &after, page_size + 1)
            .await?;
        let mut next = String::new();
        if out.len() as i64 > page_size {
            out.truncate(page_size as usize);
            let last = &out[out.len() - 1];
            next = format!("{}.{}", micros(&last.updated_at), last.id);
        }
        Ok((out, next))
    }

    /// Get returns a conversation of the caller with its messages.
    pub async fn get(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
    ) -> Result<(Conversation, Vec<Message>), Error> {
        let c = self.owned(ctx, p, id).await?;
        let ms = self.store.messages(ctx, id).await?;
        Ok((c, ms))
    }

    /// Rename retitles a conversation of the caller.
    pub async fn rename(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
        title: &str,
    ) -> Result<Conversation, Error> {
        self.owned(ctx, p, id).await?;
        let title = check_title(title)?;
        if title.is_empty() {
            return Err(invalid("a title is needed"));
        }
        self.store.rename(ctx, id, &title, self.now()).await
    }

    /// Delete forgets a conversation of the caller and its messages.
    pub async fn delete(&self, ctx: &Ctx, p: &Principal, id: &str) -> Result<(), Error> {
        self.owned(ctx, p, id).await?;
        self.store.delete(ctx, id).await
    }

    /// Append adds a message to a conversation: a user message by its owner (always done, no actions), an assistant
    /// message by a platform service. An empty role is a user message; an empty status is done.
    pub async fn append(
        &self,
        ctx: &Ctx,
        p: &Principal,
        conversation_id: &str,
        role: &str,
        mut c: Content,
    ) -> Result<Message, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        let role = if role.is_empty() { ROLE_USER } else { role };
        if c.status.is_empty() {
            c.status = STATUS_DONE.to_string();
        }
        match role {
            ROLE_USER => {
                self.owned(ctx, p, conversation_id).await?;
                if !c.actions.is_empty()
                    || !c.process_id.is_empty()
                    || c.status != STATUS_DONE
                    || !c.error.is_empty()
                {
                    return Err(invalid("a user message carries a text only"));
                }
                if c.text.trim().is_empty() {
                    return Err(invalid("a user message needs a text"));
                }
            }
            ROLE_ASSISTANT => {
                if !c.context.is_empty() {
                    return Err(invalid("an assistant message has no context"));
                }
                if !p.is_system() {
                    return Err(Error::Forbidden);
                }
            }
            r => {
                return Err(invalid(format_args!(
                    "role {} is not user or assistant",
                    quote(r)
                )));
            }
        }
        c.check()?;
        let m = Message {
            id: self.new_id("MSG-"),
            conversation_id: conversation_id.to_string(),
            seq: 0,
            role: role.to_string(),
            text: c.text,
            actions: c.actions,
            process_id: c.process_id,
            status: c.status,
            error: c.error,
            context: c.context,
            created_at: self.now(),
        };
        self.store
            .append(ctx, m, MAX_MESSAGES_PER_CONVERSATION)
            .await
            .map_err(store_err)
    }

    /// UpdateMessage replaces the content of an assistant message (a platform service only): its text, actions,
    /// status and error; the process is set when given. A cancelled message stays cancelled ([`Error::Cancelled`]
    /// otherwise): the turn that was stopped may still record what it did, never an answer.
    pub async fn update_message(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
        mut c: Content,
    ) -> Result<Message, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if !p.is_system() {
            return Err(Error::Forbidden);
        }
        if c.status.is_empty() {
            c.status = STATUS_DONE.to_string();
        }
        c.check()?;
        let now = self.now();
        self.store
            .update_message(
                ctx,
                id,
                now,
                Box::new(move |mut m: Message| {
                    if m.role != ROLE_ASSISTANT {
                        return Err(invalid("only an assistant message is updated"));
                    }
                    if m.status == STATUS_CANCELLED && c.status != STATUS_CANCELLED {
                        return Err(Error::Cancelled);
                    }
                    m.text = c.text;
                    m.actions = c.actions;
                    m.status = c.status;
                    m.error = c.error;
                    if !c.process_id.is_empty() {
                        m.process_id = c.process_id;
                    }
                    Ok(m)
                }),
            )
            .await
            .map_err(store_err)
    }

    /// Summary returns the rolling summary of a conversation (an empty Summary when it has none) to a platform
    /// service (the assistant, which writes it) or to the owner of the conversation.
    pub async fn summary(&self, ctx: &Ctx, p: &Principal, id: &str) -> Result<Summary, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if !p.is_system() {
            self.owned(ctx, p, id).await?;
        }
        match self.store.summary(ctx, id).await {
            Err(Error::NotFound) => Ok(Summary {
                conversation_id: id.to_string(),
                updated_at: from_micros(ZERO_TIME_MICROS),
                ..Default::default()
            }),
            r => r,
        }
    }

    /// SetSummary stores the rolling summary of a conversation (a platform service only): the messages up to the
    /// seq up_to are summarised in text. A summary covering fewer messages than the stored one is ignored.
    pub async fn set_summary(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
        text: &str,
        up_to: i64,
    ) -> Result<(), Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if !p.is_system() {
            return Err(Error::Forbidden);
        }
        if text.len() > MAX_SUMMARY_BYTES || up_to < 1 {
            return Err(invalid(format_args!(
                "a summary of at most {MAX_SUMMARY_BYTES} bytes covering at least one message"
            )));
        }
        let sum = Summary {
            conversation_id: id.to_string(),
            text: text.to_string(),
            up_to,
            updated_at: self.now(),
        };
        self.store.set_summary(ctx, sum).await.map_err(store_err)
    }

    /// UpdateAction changes one action of an assistant message (a platform service only), atomically with the
    /// read: f gets a copy of the action as stored and returns its replacement, or an error that leaves the message
    /// as it is. It is the compare-and-set the assistant needs to decide an action once (ADR 0090); the text,
    /// status and the other actions are untouched.
    pub async fn update_action(
        &self,
        ctx: &Ctx,
        p: &Principal,
        id: &str,
        index: i64,
        f: ActionFn<'_>,
    ) -> Result<Message, Error> {
        if p.anonymous() {
            return Err(Error::Anonymous);
        }
        if !p.is_system() {
            return Err(Error::Forbidden);
        }
        let now = self.now();
        self.store
            .update_message(
                ctx,
                id,
                now,
                Box::new(move |mut m: Message| {
                    if m.role != ROLE_ASSISTANT {
                        return Err(invalid("only an assistant message is updated"));
                    }
                    if index < 0 || index as usize >= m.actions.len() {
                        return Err(Error::ActionMissing);
                    }
                    let i = index as usize;
                    let a = f(m.actions[i].clone())?;
                    let mut acts = m.actions.clone();
                    acts[i] = a;
                    let c = Content {
                        text: m.text.clone(),
                        actions: acts,
                        status: m.status.clone(),
                        error: m.error.clone(),
                        ..Default::default()
                    };
                    c.check()?;
                    m.actions = c.actions;
                    Ok(m)
                }),
            )
            .await
            .map_err(store_err)
    }
}

/// Go's zero time in unix microseconds: the `UpdatedAt` of a conversation's summary before one is written.
const ZERO_TIME_MICROS: i64 = -62_135_596_800_000_000;
