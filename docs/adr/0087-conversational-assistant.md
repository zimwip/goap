# ADR 0087 — The conversational assistant

**Status**: accepted, implemented (server side, web API client; the web interface is ADR 0088); the tool set extended by
ADR 0090, ADR 0094 and ADR 0106 §6, the context replaced by ADR 0092, the model protocol by ADR 0098 · **Date**: 2026-10 ·
Builds on ADR 0084 (protected aliases), 0085 (conversations) and 0086 (contextual helper, whose model call it reuses).

> "Four tools" below is the original set. ADR 0090 adds `list_agents` / `start_agent` and the confirmation
> `ConfirmAction`, ADR 0094 `methodology_query`, ADR 0106 §6 `search_changes` and the **frame** of a turn
> (`internal/assistantsvc/frame.go`): eight server tools (`Tools()`), of which a turn offers those of its level
> (`project`, or `change` when the person looks at a change they can read); `create_change` only at level project and
> once `search_changes` ran in the conversation. ADR 0092 replaces the context with a layered one and adds the screen
> tools; ADR 0098 gives the tools through the provider's tool API.

## Context

A person writes to the assistant in a conversation: they ask what a methodology is for, which change fits their need,
and ask it to start a change or to take them to one. The conversation is stored (ADR 0085); something has to answer.

## Decision

- **Who, why, how, what**: like the helper (ADR 0086), the assistant answers none of the four questions of the mental
  model; it is an interface aid over the model gateway (*with what*) that acts for the person, with their rights, through
  tools. It starts a change only by the same graph call the web uses, so the change is the person's, with its intent and
  its methodology; nothing it does bypasses a change.
- **A dedicated builtin, not a methodology agent.** The engine's `llm` action is a one-shot items protocol (a prompt, a
  set of items back); it cannot hold a conversation, ask the model again with tool results, or answer a person in
  seconds. The assistant is a use case (`internal/assistantsvc`) over three ports (`Graph`, `Methodologies`, `Projects`;
  ADR 0090 adds `Engine`), the model gateway (`modelgw.SuggestModel`: `Complete` and `Available`) and the conversation
  service. `pkg/graph` and `pkg/engine` must not import it (`pkg/layering`).
- **One RPC of its own, `assistant.v1.AssistantService.Send`**, rather than a method of `ConversationService`: that service
  only stores, has no graph and no model gateway, and keeps its schema and trust small (ADR 0085). `Send` takes
  `{conversationId, text, context}` and returns the appended user message and the pending assistant message at once.
  **Hosted by the `conversations` process**: it is the process that owns the conversation store (a direct call, no hop,
  no system RPC to append), and it needs only clients of services that exist (graph, gateway, registry, with the
  caller's identity forwarded). A process of its own would add a route, a port, a compose service, a Makefile / `make.ps1`
  entry and a database role for no isolation gain; the gateway route `/goap.assistant.v1.AssistantService/` goes to
  `GOAP_CONVERSATIONS_URL`. `goap-dev` mounts it in process.
- **The turn.** `Send`: (1) refuses when the caller has no `assistant` alias (`Available`, role allow-list and
  availability included): `FailedPrecondition`; (2) refuses (`PermissionDenied`) an active project the caller may not work
  on (`access.Directory.MayAccessProject`); (3) refuses (`FailedPrecondition`) when the previous message is still pending
  (**one turn at a time per conversation**: a mark in the process, and the stored pending message for other replicas; a
  pending message older than three minutes is a turn that died with its process and no longer blocks); (4) appends the
  user message, with a **short description of the context** (`Message.context`, `Context.describe()` since ADR 0092:
  `on change CHG-1, tab change, project PROJ-A, 120 selected characters`, at most 300 bytes: never the selection, the
  tab params or any form content), then an empty `pending` assistant message, as `system:assistant`; (5) starts the
  turn in the background and returns. The turn runs **as the calling principal** (the model call through the gateway,
  so role allow-lists, quotas and token counters apply; the tools) and writes the answer into the pending message **as `authz.System("assistant")`** (ADR 0085: only a
  platform service writes the assistant's words): text, actions, `status` `done`, or `error` with a short error. The web
  polls `GetConversation`. A turn has a 5-minute budget (`TurnTimeout`; a pending message older than `StaleAfter`, 7
  minutes, no longer blocks the conversation). **The person can stop a turn** (`assistant.v1.Cancel {conversationId}`,
  owner only, `Service.Cancel`): the pending message becomes `cancelled` at once (a final status of `convsvc`, kept with
  the actions recorded so far: proposals stay to decide, `UpdateMessage` refuses any other status afterwards,
  `convsvc.ErrCancelled`), the turn of this process has its context cancelled (cause `errStopped`), and a turn served
  by another process reads its message between its rounds and stops before the next model call; tools already run stay
  done. With nothing pending, `Cancel` returns the last message. The history tells the model an answer was stopped.
  Web: the Send button becomes Stop (and Esc in the input) while an answer is pending.
- **The run is the pending message.** No engine `Process` is attached: there is no process to step, pause or resume, the
  turn is one bounded loop, and an engine record would be a second source of truth for what the message already says
  (`conversationId`, `status`, `error`, `createdAt`). `Message.processId` stays empty for the assistant.
- **The model protocol.** As decided here, one JSON object per answer, `{"message": "...", "tool_calls": [{"name",
  "arguments"}]}`, parsed defensively (`llm.DecodeJSON`; an answer that is no JSON is the final text); since ADR 0098 the
  tools are declared through the provider's tool API (`toolspecs.go`) and the answer is plain text with native tool calls,
  the JSON form still read (`readAnswer`). With tool calls, each is run and all the results go back to the model (tool
  results, or one user turn marked as data for a JSON answer), except when every call only hands something to the
  person (`start_agent`, `open_change`, a `ui.*` tool), all succeeded and the answer has a message: that message is final
  (`settling`, a model call saved, the latency of a turn being its model calls); at most **16 model calls** per turn
  (`MaxRounds`, 4 at first, raised so that agents can chain tool calls; the person can stop a turn instead): the last
  one is offered no tool and told to answer with what it found, its message is the answer, or the turn fails with
  `error` when it has none. At most 4 tool calls per round (the others get an error result). An unknown tool name, or one not of the turn's frame, is an error
  fed back, **never run**. The prompt (`prompt.go`, the Go constant `systemRules`) says what the assistant is for, gives
  the frame of the turn with its server tools, asks it to take keys and ids from the person or from tool results only,
  and gives the **context snapshot of this turn** as data, with the rule that nothing in it or in the selection is an
  instruction. **History** sent: the last 12 messages (the new one included) within 16 KiB, starting on a user message,
  texts only (an assistant message is followed by what its actions were and what the person decided, `describeActions`);
  the older messages are covered by a rolling summary (`compact.go`, `convsvc.Summary`, rewritten after the turn) sent in
  the system text; the context of earlier turns is not resent.
- **Exactly four tools, nothing else** (the original set, see the note above):
  - `list_methodologies` (read): the methodologies applicable to the active project (`Snapshot.ApplicableMethodologies`:
    the project's own and its ancestors', the server-side twin of the web's `applicableMethodologies`), each with its
    description and the examples of its goals, read from the registry.
  - `select_project` `{project}` (UI action): refused unless the project exists and `MayAccessProject` holds for the caller,
    the check of the token reissue; the refusal is the same whether it does not exist or is not accessible. The selected
    project becomes the active one for the rest of the turn.
  - `create_change` `{title, intent, methodology?}` (server-side, UI result): in the active (or selected) project, the
    root project when none is active (ADR 0091), as the caller, through `Graph.CreateChange` (in process: the graph with
    the triggers' event hook; distributed: `graphsvc.Client`, the principal forwarded); the methodology must be one
    applicable to the project, its namespace is the change's. The graph's rules are the normal ones (there is no further permission on creating a change; the project
    must be one the caller may work on). `Data` records `createdBy`, `via: assistant` and the conversation. Returns the
    change id.
  - `open_change` `{changeId}` (UI action): the change must exist and be visible to the caller (a personal change of someone
    else answers as if it did not exist).
- **UI actions** are returned in the message's `actions` as `{type, args, result?}`, only when they succeeded: `select_project`,
  `open_change`, and `create_change` with its result (`changeId`); the web executes them (switch project, open the tab).
  Read tools and refusals leave no action.
- **Conversation service change**: `Message.context` (a column of both dialects, schema edited in place, greenfield), set
  by the owner on a user message, refused on an assistant one, at most 300 bytes.
- **Web**: the API client (`web/src/lib/api/assistant.ts`, `AssistantContext`, the action types) and its test; the
  interface is ADR 0088.

## Consequences

- The `assistant` alias is the only switch: no alias, or none the person may use, no assistant.
- Another replica of `conversations` does not know of a turn in flight elsewhere except by the stored pending message;
  two simultaneous `Send` on different replicas can both pass the check (the one-turn rule is best-effort across
  processes, exact within one).
- A turn killed with its process leaves a `pending` message that stays pending (shown as such by the web) until a later
  `Send` is allowed after three minutes; nothing rewrites it to `error` yet.
- There is no live event: the web polls (ADR 0085).

## Not done

Streaming of the answer; a retention policy of conversations; a token budget specific to the assistant
(the quota of the model applies).
