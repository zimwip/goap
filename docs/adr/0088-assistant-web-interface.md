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
  then done; a reloaded conversation never replays old actions. Each action is a chip that repeats it on demand.
- **Context** is the active tab (kind and params), its subject, the selected text and the active project, capped in bytes
  as the server does; no form value is ever sent (the helper's fields are not read).
- **One switch.** `aliasFlags.assistantEnabled` hides the launcher and the commands; the tab explains and links to the
  model catalog. The helper keeps its own flag. The launcher and the helper are never open together.

## Not done

Streaming, a launcher badge for an answer that arrives while the panel is closed, conversation search.
