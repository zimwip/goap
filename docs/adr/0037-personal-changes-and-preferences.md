# ADR 0037 — Personal changes, purging an unapplied change, staged settings

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0001 (the change), ADR 0024 (change impacts), ADR 0029 / 0030 (event-sourced impacts, one change log),
ADR 0032 (branches).

## Context

The settings of the platform (LLM providers, models, quotas, aliases) are graph data, and every modification of the
graph goes through a change. The edits of a settings dialog are nobody else's business and tentative until the person
saves them: a change that is never applied must be able to disappear, which the insert-only log of ADR 0030 did not
allow. (The personal preferences of a user are not graph data: ADR 0038.)

## Decision

### 1. Personal changes

A change whose owner unit is a **personal unit** (`USR:<subject>`, an `OrgUnit` of kind `personal`, created on first
use; `owner_org: "@me"` at creation resolves to the caller's) is *personal*: only that subject reads, writes, applies
or removes it (the Connect interceptor `Handler.PersonalScope` answers "not found" to anyone else, administrators
included; `ListChanges` / `ListNodeChanges` leave it out), and it is never split into sub-changes nor a sub-change.
The personal unit is written directly, like the seed does: a unit is not what a change decides.

### 2. Purging a change that landed nothing

Nodes are never deleted once used: a removal is a tombstone version. A **change**, on the other hand, can be removed
with its whole `change_log`, impacts, branch and unapplied node versions **as long as nothing of it is in the graph**
(`Graph.PurgeChange`, `Tx.DeleteChange`, RPC `DeleteChange`). It is refused (`ErrConflict`) when the change is
applied or merge pending, has sub-changes, produced a baseline, its branch received a merge, or anything uses what it
wrote (a baseline entry, a later version of a node, a link of another change). This is the one exception to the
insert-only log of ADR 0030: what is removed was never part of the graph.

The purge has no policy hook: nothing needed one, and a rule keeping discarded changes would be a new extension point
added when a use case asks for it (it was `Graph.PurgePolicy`, removed unused, ADR 0065).

### 3. The settings dialog stages its edits

Everything the settings dialog edits is graph data of the **platform** (LLM providers, models and quotas, aliases) or the **organisation** namespace. An edit opens, behind the scenes, a personal change of the
namespace of the node (own branch, held by the personal unit of the user) and is written as an impact of it: one
impact per node, a new version per edit. The dialog shows the graph with the staged nodes laid over it, marked
*unsaved*. The **Save / Discard bar is always in the dialog**: Save accepts the impacts and applies the changes;
Discard, or leaving the dialog and accepting the warning that the edits will be lost, purges the changes. An unsaved
change survives a reload and is offered again at the next load. Only graph functions are used; the engine is not
involved (`web/src/lib/stores/pending.svelte.ts`, `settings.svelte.ts`, `llmEdit.ts`).

### 4. Proposals are reviewed in the dialog

A change other than the user's own that leaves an impact *proposed* shows up in the matching section with **Accept /
Decline**. Today: an LLM alias that a methodology references and nobody configured (`ensureAliasStubs` opens a
platform change proposing a stub, ADR 0021) is listed in *Models & quotas* as an alias without target; Accept picks
its target, accepts the impact and applies the change, Decline rejects it and abandons the change.

## Consequences

- Other kinds of personal data (saved searches, layouts) reuse the same mechanism: a node type, a personal change.
- A personal change is invisible to the journal of others and to the engine's list of changes: nothing can be reported
  on it by anyone but its owner.
- The engine service does not yet apply the personal scope to its own change endpoints; processes never run on a
  personal change.
