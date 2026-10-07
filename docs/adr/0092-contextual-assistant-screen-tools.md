# ADR 0092 — A contextual assistant: layered context and screen tools

**Status**: accepted, implemented (server side and TS types; the collector and the tool registry of the web are a later
task) · **Date**: 2026-10 · Builds on ADR 0085 (conversations), 0086 (helper), 0087 (assistant), 0089 (ledger),
0090 (proposals, `ConfirmAction`).

## Context

The assistant knew the active tab, a subject id and the selected text: enough to name the change the person looks at, not
to act on what is on the screen. The person wants it **contextualised** to the open screen so it can act on its
elements, with the tools **refined per screen**, and a context that **zooms** on the action in progress instead of
describing the whole page evenly.

## Decision

- **Who, why, how, what**: the assistant still answers none of the four questions. It helps the person operate the
  interface; every data change keeps going through the screen's own edit path (hence through a Change, the graph's and
  the engine's checks) and the person's confirmation.
- **Layered context** (greenfield, replaces the flat one), rendered in the prompt most specific first:
  `focus` (the element the person acts on, the selection, the open dialog, `pendingAction`, the `errors` on screen,
  `lastAction`), then `screen` (`kind`, `title`, a `summary`, up to 40 `entities` `{type, id, label, state?, props?}`),
  then `app` (`activeProject`, `tab`). Absent fields are omitted. Caps and sanitising are the server's
  (`Context.normalise`): 8 KiB rendered, 40 entities (the entity of the focus is moved first so it is the last one
  dropped; entities are dropped from the end until the context fits), 10 errors, 6 props, every text cut (labels 200
  bytes, summary 600, actions 300, errors 200), control characters blanked; only a selection over 2000 bytes, or a
  context that does not fit even without entities, is refused. It stays data: the prompt says it is never instructions.
  **Persistence is unchanged**: the user message keeps a short description derived from focus, screen and app
  (`reviewing impact X of change Y, on impact X, dialog ..., screen ..., tab ..., project ..., N selected characters`),
  never the snapshot; the ledger source stays `assistant`. The change of a turn (for `list_agents` / `start_agent`) is
  the `change` tab's `id` param, else a focused `change` element (`subject` is gone).
- **Screen tools**. `Send` also receives `uiTools[]`, the tools the CURRENT screen offers this turn: `{name, description,
  guidance, level effect|write, args, target?}`. `args` is a typed subset of JSON Schema (a protobuf message, not free
  JSON): properties of type `string | number | boolean | enum | array` (an enum lists 1 to 30 values; an array has
  `items`, a scalar or enum, never nested), `required`. Caps: 30 tools, names `[a-z][a-z0-9_.-]{0,48}` unique, a
  description of 300 bytes, a guidance of 200, 12 properties named `[A-Za-z][A-Za-z0-9_]{0,39}`, 2 KiB of schema per
  tool. An invalid descriptor refuses the request (`InvalidArgument`): it is a bug of the screen, not of the model. The
  model sees them in the prompt and calls them `ui.<name>` in the same tool-call protocol, so a screen tool can never
  shadow one of the server tools (ADR 0090 and 0094 are untouched). A call to a name not in this turn's
  descriptors, or whose arguments fail the schema (unknown property, missing required, type, enum, array element,
  4 KiB of arguments, 2000 bytes per string) is an error fed back to the model and never emitted. **The server never
  executes a screen tool.**
- **Effect and write**. `effect` tools change no data (navigate, filter, select, open, scroll, focus a field): they are
  emitted as `{type: ui_tool, status: requested, level, tool, args, label, rationale?, target?}` for the web to run at
  once; the model is told "requested on the client, no result available" and must not rely on the outcome. `write`
  tools modify something: they are recorded as a proposal `status: proposed` (plus `project`), nothing runs, like
  `start_agent`. Per answer: at most 3 proposals and 5 effects (the rest is an error to the model). The label is the
  descriptor's description with the arguments; the optional `rationale` is a sibling of `arguments` in the call.
- **Confirmation**. `ConfirmAction` handles `ui_tool` next to `start_agent` (that branch is unchanged): reject moves
  `proposed` -> `rejected`, always possible; accept moves it to `accepted` and the server does nothing else (no engine,
  no graph); the web then runs the tool through the screen's normal edit path, as the person. Compare-and-set as
  before (`ErrDecided`, Aborted). A proposal is **stale** (accept refused with `ErrStale`, still rejectable) when the
  active project of the request differs from the project recorded in the proposal.
- **Outcome**: `assistant.v1.AssistantService.ReportAction {conversationId, messageId, actionIndex, status done|failed,
  error?}` (conversation owner only, forbidden for a service). Allowed from `accepted` (a write) and from `requested`
  (an effect: optional, cheap), recorded once through the same compare-and-set (`reported {by, at}`; another status is
  `ErrState`, FailedPrecondition; an outcome already reported `ErrDecided`, Aborted; a `rejected` or `proposed` write
  cannot be reported). The next turn's history says, per screen tool: asked (no outcome known), proposed (not decided),
  accepted (the interface is applying it), rejected, done, failed with the error, so the model follows up.
- **Helper (ADR 0086)** is untouched here (`modelgw.Suggest` unchanged); the web phase unifies the field registry with the
  screen tools.
- **Prompt**: documents the layers (focus > screen > app), the `ui.` tools and their guidance, effect versus write, the
  confirmation rule (never claim a write was done before it was confirmed), at most N per answer, and asks a short
  question when the focus is ambiguous.

## Consequences

- The model can operate the screen but never on its own authority: effects touch no data, writes need a click and are
  applied by the screen's own path with the person's rights; the server holds no UI vocabulary beyond descriptors.
- Descriptors travel with every message (up to 30 tools, about 60 KiB worst case): the screen decides what is offered, so
  the tool set is refined per screen and per state (a disabled action is simply not offered).
- A `write` accepted whose outcome is never reported stays `accepted`; the history says so.

## Not done

The web collector (zoom on the action in progress, entities of each view), the registry of screen tools and the
proposal card with Accept / Reject for `ui_tool`; expiry of unanswered proposals; reconciling the helper's field registry
with the descriptors.
