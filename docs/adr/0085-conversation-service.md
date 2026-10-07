# ADR 0085 — The conversation service

**Status**: accepted, implemented (service, gateway route, web API client; the assistant that fills it is not) ·
**Date**: 2026-10 · Modelled on ADR 0038 (user preferences service).

## Context

The assistant of the web talks with a person: they ask, it answers and may ask the web to do things (select a project,
open a change, create a change, list methodologies). The thread of that talk is personal, grows with the use and is not
what describes the enterprise: it is no graph data and needs no change, no review, no version (rule 4 of the mental
model keeps the graph for what describes the enterprise). It must survive a reload and be read from several devices,
so it cannot stay in the browser.

## Decision

- **A service of its own, shaped like `prefssvc`** (`internal/convsvc`, `cmd/conversations`, Connect
  `conversations.v1.ConversationService`, gateway route, port 8090): its own store, `MemoryStore` and `SQLStore`
  (SQLite local mode / PostgreSQL), its own `migrations/` and `migrations_sqlite/` kept aligned by `TestSchemasAligned`.
  Tables `conversation` and `conversation_message` (times in unix microseconds).
- **Model**: a `Conversation` {id, subject, title, createdAt, updatedAt} owned by one subject; `Message` {id,
  conversationId, `seq` (from 1, given by the store in the transaction that appends), `role` `user` | `assistant`, text,
  `actions` (a JSON array of `{type, args, result?}` the assistant asked the web to run), `processId` (the engine run
  that produced an assistant message), `status` `pending` | `done` | `error`, error, createdAt}. The service does not
  interpret `type` or `args` (the web owns that vocabulary, as it owns the preference keys, ADR 0068); it only requires a
  `type`.
- **Who may do what**: the subject of a request is the caller's, never named by the request. The owner lists (newest
  update first, paged by an opaque token), reads, renames, deletes and appends **user** messages. A conversation of
  someone else answers *not found*. A **system principal** (`authz.System`, the executor of the assistant) appends
  **assistant** messages to any conversation and updates them (`UpdateMessage`: text, actions, status, error, process
  replaced), but lists, reads, renames and deletes nothing, and a person cannot write the assistant's words.
- **Limits** (`ErrInvalid`, `InvalidArgument`): 200 conversations per subject, 500 messages per conversation, 64 KiB of
  text and of actions per message, 200 bytes of title, 4 KiB of error.
- **No event.** The hub (ADR 0053) is fed by the graph and the engine through `domain` events it translates; a
  conversation event would need a new publisher in this service, a subject, a translation in the hub and a reducer in
  the web, for a signal a few lines of polling give. The web polls `GetConversation` while a message is `pending`; a
  `conversation.updated` signal can be added when the executor lands, publishing from `Service` like the graph does.

## Consequences

- Deleting a user does not delete their conversations (nothing links the graph to this service); a later retention
  decision belongs to this service.
- Cross-service reads (the executor needing the history) go through the web request that starts the run, not through a
  system read, so the platform never reads a person's conversations by itself.
