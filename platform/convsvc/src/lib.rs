//! The conversations of the users with the assistant, kept
//! outside the graph (ADR 0085): per subject, conversations holding ordered messages, read and written by their owner
//! alone (the executor of the assistant, a platform service, appends and updates the assistant's messages).
//!
//! - [`Service`]: who may do what, and the limits.
//! - [`Store`]: [`MemoryStore`] and [`SqlStore`] (SQLite for the local mode, PostgreSQL), with their migrations
//!   [`MIGRATIONS`] / [`SQLITE_MIGRATIONS`].
//! - [`Handler`]: the Connect `ConversationService` (`goap_gen::conversations::v1::conversation_service_router`);
//!   [`message_to_pb`] the wire form of a message.
//!
//! Differences from the Go package: Go's `SQLStore{DB, Dollar}` is the enum [`SqlStore`]; the sentinel errors are
//! the variants of [`Error`] (`errors.Is(err, ErrInvalid)` is [`Error::is_invalid`], true for the errors wrapping
//! it); an error the function given to [`Service::update_action`] returns travels as [`Error::Other`].

mod handler;
mod service;
mod store;

pub use handler::{Handler, message_to_pb};
pub use service::{
    Clock, Content, DEFAULT_PAGE_SIZE, DEFAULT_TITLE, Error, MAX_ACTIONS_BYTES, MAX_CONTEXT_BYTES,
    MAX_CONVERSATIONS_PER_SUBJECT, MAX_ERROR_BYTES, MAX_MESSAGES_PER_CONVERSATION, MAX_PAGE_SIZE,
    MAX_SUMMARY_BYTES, MAX_TEXT_BYTES, MAX_TITLE_BYTES, NewId, Service,
};
pub use store::{
    Action, ActionFn, Conversation, Cursor, MIGRATIONS, MemoryStore, Message, MessageFn,
    ROLE_ASSISTANT, ROLE_USER, SQLITE_MIGRATIONS, STATUS_CANCELLED, STATUS_DONE, STATUS_ERROR,
    STATUS_PENDING, SqlStore, Store, Summary,
};
