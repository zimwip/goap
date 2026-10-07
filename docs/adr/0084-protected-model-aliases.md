# ADR 0084 — Protected model aliases

**Status**: accepted, implemented (phase 1: aliases, guard, seed, web availability; no assistant UI yet) · **Date**: 2026-10 ·
Builds on ADR 0021 (model gateway configuration as graph data), ADR 0048 (node validators), ADR 0076 (nodes are retired, never deleted).

## Context

The platform itself is about to call models by name: a conversational assistant and a contextual helper that fills fields.
Aliases are ordinary `platform@LlmAlias` nodes an administrator may retire or rename, and `SeedModels` only runs on a graph
holding no provider, so an existing install would not have them.

## Decision

- **`protected` attribute** on `platform@LlmAlias` (`llmcfg.Alias.Protected`, `ModelAlias.protected` of the model proto):
  an alias the platform uses itself. It may be retargeted, never retired, renamed or unflagged. Names:
  `llmcfg.AssistantAlias` (`assistant`), `llmcfg.HelperAlias` (`helper`).
- **Guard**: `llmcfg.ProtectedAliasValidator`, a `graph.NodeValidator` wired in `cmd/graph` and `cmd/goap-dev` next to the
  admin floor. It compares each alias version a change writes with the one it replaces (retired state, flag, `alias`
  name) and requires the platform's names to be created protected. `pkg/graph` names no alias; the refusal is
  `graph.ErrInvalid`.
- **Seed**: `graphsvc.SeedProtectedAliases`, one step of `Boot` after `SeedModels`, idempotent: a missing alias is created in
  one change with the target of `default` (the helper: of `fast` when present); a node of that name without the flag or
  retired is flagged and restored. An existing protected alias is never touched, its target is the administrators'.
- **No model configured**: the alias exists with an empty target (valid only when protected). The snapshot keeps it without
  a problem, the router resolves nothing, `ListModels` omits it, `ListCatalog` lists it with no provider and model so an
  administrator can retarget it. Same when its model or provider is later retired: the web does not retire a protected
  alias with them (`deleteModel`, `deleteProvider`), the server guard holds regardless, and the alias becomes unavailable.
- **Availability** for the web: `aliasAvailable(name)` and `aliasFlags.assistantEnabled` / `helperEnabled`
  (`stores/modelChoices.svelte.ts`), read from `ListModels` and refreshed with the other model choices when the model
  configuration changes. Enabled means the alias resolves to a model the caller may use.

## Not done

The assistant and the helper themselves; an alias rename is impossible, not offered by the catalog pane.
