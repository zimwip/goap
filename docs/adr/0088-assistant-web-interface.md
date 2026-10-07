# ADR 0088 — The assistant's web interface

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0085 (conversations), 0086 (helper) and 0087
(conversational assistant, whose web interface it completes).

## Context

ADR 0087 left the interface to a later phase. The earlier assistant kept a local history in `localStorage` and started a
process from the intent; it is replaced by the conversation service.

## Decision

- **One view, two places.** `Assistant.svelte` shows the conversation as a tab (`assistant`) and as a compact floating
  panel opened by a launcher bubble on every screen (`Ctrl+Shift+A`, command "Ask the assistant"). The panel offers a
  button to continue in the tab. The old left-zone panel is removed.
- **State in memory.** The conversation service is the only store (ADR 0085). The current conversation id is **not** kept
  in `localStorage` (ADR 0052: where a person was is not browser state); a new page opens the most recent one. The draft
  is shared by the tab and the panel.
- **Polling.** After `Send` the store polls `GetConversation` every 1.5 s while the last message is pending, only while a
  view is mounted and the page visible, backs off (up to 15 s) on errors and gives up after six, drops a response that
  comes back for an earlier conversation, and stops on `done` / `error`.
- **Actions run once.** The web executes the actions of a message only when it saw that message pending in this page and
  then done; a reloaded conversation never replays old actions. An action that only navigates is a chip that repeats it on
  demand; a proposal is a card (below).
- **Context**: see "Screen tools and layered context (ADR 0092)" below (it replaced the flat context of this first phase).
- **One switch.** `aliasFlags.assistantEnabled` hides the launcher and the commands; the tab explains and links to the
  model catalog. The helper keeps its own flag. The launcher and the helper are never open together.

## Screen tools and layered context (ADR 0092, web phase)

- **One registry**, `web/src/lib/assist/registry.svelte.ts`, replaces the helper's field registry (ADR 0086; `helper/fields.svelte.ts`
  is gone, `use:assistField` lives in the registry and the helper still builds its `Suggest` request from the fields of the
  active tab). For as long as a view is mounted it registers, per tab and with automatic cleanup (`registerAssist`,
  `use:assistField`, `use:assistTarget`, `use:assistTool`): a **screen provider** (`kind`, `title`, `summary`, `entities`), a
  **focus provider** (element, dialog, pending action, errors), **screen tools** and **fields**. `describeTools()` yields the
  descriptors for `Send` (a tool the view disables is absent; a descriptor the server would refuse is dropped, never sent),
  `runTool(name, args)` runs one. The fields of a tab also become entities of the screen layer (name, label, type, enum,
  guidance, never the value) and one derived write tool, `set_field {field, value}` (the helper's setter, the value coerced to
  the type of the field), except fields a dedicated tool fills (`FieldSpec.tool`).
- **One catalog**, `assist/catalog.ts`, holds the descriptor of every tool (description, one-line guidance, level, arguments)
  and the guidance of the fields that are no node attribute (an attribute's tooltip is its guidance; the default is never empty).
  A view registers an implementation by tool name; a name not in the catalog is refused, so no tool reaches the model without
  guidance. `catalog.test.ts` checks each entry against the server's caps.
- **The web never runs what is not registered**: the arguments are validated against the descriptor in force now
  (`assist/schema.ts`, the server's rules), a tool no longer on the screen reports `failed` with a clear error.
- **Context collector** (`assistant/context.ts`): the app layer always, the screen layer of the active tab's views, the focus
  layer from the views plus what the page focus was. It zooms: the focused entity first, the entities a view ranks first (the
  impacts awaiting review) next, everything cut client-side to the server's caps and a 7 KiB budget from the end. The
  selection and the focused element are captured as they happen (`assist/capture.ts`: `selectionchange` / `focusin` outside
  the assistant, and when the input takes the focus), because clicking into the input clears them; the last non-empty one within
  60 s is used at send time and forgotten after the send. `lastAction` comes from a tiny recorder (`assist/recorder.ts`) the
  screens feed from their edit paths. A form value never leaves except a field flagged `share` while it has the focus.
- **Executor** (`assistant/actions.ts` `runActions(message, report)`, store `apply`): an `effect` `ui_tool` seen `requested` runs
  at once through the registry and its outcome is reported (best effort); a `write` proposal and a `start_agent` run nothing
  there.
- **Proposal card** (`views/assistant/ProposalCard.svelte`, pure view in `assistant/proposal.ts`, decisions in
  `stores/proposals.svelte.ts`) for `start_agent` (methodology, agent, goal, intent, target change or new change, rationale;
  Launch / Reject) and for write `ui_tool` proposals (label, arguments, rationale; Apply / Reject; the target element is scrolled
  into view and outlined while the card is hovered or focused). Statuses: proposed, starting, started, applying, accepted-not-
  applied, done, failed (with the error), rejected. Accept is `ConfirmAction` with the active project; for a write the tool then
  runs through the registry and `ReportAction` records done or failed. FailedPrecondition shows "no longer valid, you can reject
  it", Aborted reads the conversation again. After a reload an `accepted` write with no reported outcome shows "accepted, not
  applied" and a Retry, offered only while the tool is registered. A started agent shows the status of its run (running, waiting
  for approval, completed, failed...) read from the process by `result.processId` (`AgentRunStatus.svelte`, the live process
  store, no mirroring) with links to the change and to the run tab. A server tool this web does not know still shows a passive
  chip (its label, else its type).
- **First screens**: the change tab (screen: title, status, lifecycle state, project, counts; entities: the impacts, awaiting review
  first; tools `select_impact`, `open_impact`, `filter_impacts` (effects), `review_impact`, `rename_change`, `update_intent`,
  `move_change` (writes), each through the functions the screen already uses, enabled only when the screen offers the action);
  the node property form (its attributes as fields, `set_field`); the new change form (`set_title`, `set_intent`, `set_project`
  fill the form and are effects, `create` is a write); the review comment fields (impact review in the change table and the node
  tab, the review object's comment and entry comments) as fields with guidance, and the impact with the names of the properties
  it edits as the node tab's screen context. The change tab's lifecycle transitions have a `transition_change` write tool (ADR 0058, ADR 0092).
- **One helper at a time** is unchanged: the panel closes when the helper opens and the shortcut closes the helper first.

## Not done

Streaming, a launcher badge for an answer that arrives while the panel is closed, conversation search.
