# ADR 0038 — User preferences live outside the graph

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0037 (personal changes), ADR 0020 (users and policies in the graph).

## Context

The first design made the preferences of a user (theme, voice input, dashboard defaults) graph data, changed through a
personal change with a Save button. That is the wrong tool: what a person likes is theirs alone, changes often (the
theme is a shortcut in the header), must apply as soon as it is chosen, and is nobody's decision to review or version.
The graph describes the enterprise; the enterprise does not care that a user prefers the dark theme.

## Decision

- **The user is declared in the graph** (`organisation@User`); **their preferences are not**. A new service,
  `preferences` (`cmd/preferences`, `internal/prefssvc`, Connect `preferences.v1`), keeps one JSON document per subject
  in its own store (`user_preference`; PostgreSQL, SQLite in local mode, memory). It is routed by the gateway and run
  in process by `goap-dev`.
- **A caller only reaches their own document**: the subject is the one of the request identity, there is no way to
  name another. `GetPreferences`, `SetPreferences` (merge, a null value clears a key), `ResetPreferences`.
- **The service keeps an opaque document** (ADR 0068 replaced the `prefssvc.Schema` of this ADR): any key and value
  of JSON, size-limited; the interface (`preferences.svelte.ts`) validates and defaults the keys it knows
  (`theme`, `voiceEnabled`, `voiceModel`, `voiceLanguage`, `usagePeriod`, `usageScope`), a new preference touches no
  backend code.
- **No change, no Save.** The interface applies a preference at once and writes it to the service in the order it was
  made; the last values are also kept in the browser, so the interface starts the way the user left it, and the
  service is the reference read at start. The theme selector is back in the header user menu as a shortcut.
- **Platform settings stay graph data** (ADR 0037 §4): providers, models, quotas, aliases are still staged in personal
  changes and saved from the dialog's Save / Discard bar. The dialog's Preferences section is not part of that bar.

## Consequences

- `platform@UserPreference` is not a node type: nothing of the preferences is versioned or journaled in the graph.
- Deleting a user from the graph leaves their document behind; it is harmless and can be removed with
  `ResetPreferences` by that user or by a housekeeping job.
- The service is a service of its own only for the separation of what is the enterprise (graph) from what is personal
  (here); it holds no secrets and can be scaled or replaced independently.
