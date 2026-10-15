# ADR 0120 — Settings staged by the server

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0037 (personal changes, staged settings), ADR 0079
(drafts), ADR 0102 (only a modification is accepted), ADR 0105 (verdicts made by a submitted review)

## Context

The settings dialog stages its edits in personal changes (ADR 0037 §3). The web drove every step itself: an edit read
the head of main, looked the node up in a baseline, proposed an impact, read the blackboard to know whether a draft
existed, checked the node out and updated its draft (three to six calls); removing a provider retired its aliases and
models one call sequence at a time; loading read the changes, then a blackboard per change and a node per impact; Save
read each change, withdrew and reviewed impact by impact, then applied. The browser's network log showed a long train of
calls for one click, each one a round trip and a transaction of its own, and the rules of the staging (when a node is
created, restored, given up) lived in the web only.

## Decision

The graph service does the staging (`internal/graphsvc/settings.go`), four RPCs of `GraphService`, each answering the
whole pending state of the caller (`PendingSettingsResponse`: one `PendingSettings` per namespace, its change and its
`StagedSetting` nodes: key, type, draft properties, `retire`, `created`, `change_impact_id`, `written`):

- `GetPendingSettings`: the open personal changes of the caller (`ListChanges` on `USR:<subject>`), one per namespace,
  their main-flow impacts read through the change.
- `StageSettings {edits: [{namespace, type, key, props | retire}]}`: the edits in order. The personal change of a
  namespace is opened on main's head at the first edit. An upsert creates a node the change does not see
  (`ImpactNodeCreate`), restores a retired one (transition to `active`), checks out a node with no draft (by key, which
  declares the impact) and merges the properties into the draft. A retirement withdraws a node the change created
  (the change is purged when it holds nothing more), cancels the draft of an existing node and transitions it to
  `retired`. The first refused edit stops the call; the ones before it stay staged.
- `SaveSettings`: per change, the impacts without a draft or whose draft is the version it was checked out from are
  withdrawn (`WithdrawImpacts`, one call), the others accepted in one review batch by their owner
  (`ImpactNodeReviewBatch`), then the change is applied (`Apply`, `change.applied` published); a change left with
  nothing is purged.
- `DiscardSettings`: the changes are purged.

The gates are those of the RPCs replaced: the creation of a node is authorized like `ImpactNodeCreate` (`checkCreate`),
every node of an `adminOnly` type needs the access permission (`gateAccess`), the graph's operation authorizer runs in
each graph call, the type an edit names must be the node's. A caller with no subject is refused (Unauthenticated). The
calls of one subject are serialized in the process (an edit builds on the pending state it reads).

The web store (`web/src/lib/stores/pending.svelte.ts`) keeps its interface (`stageUpsert`, `stageRetire`, `stagedOfType`,
`pending`, `savePending`, `discardPending`, `loadPending`) and adds `stageEdits` for a batch; it holds what the server
answers (`fromServer`) and, after a refused call, reads the pending state again. `llmEdit.ts` sends the removal of a
provider or a model with its aliases, and the save of several models, as one batch.

## Consequences

- One call per edit or batch of edits, one for loading, one for Save, one for Discard; the staging rules live in one
  place, next to the graph.
- The graph calls behind one RPC are still separate transactions: a failure in the middle of a batch or of Save leaves
  what was done before it (as the dialog did), and the answer of the next read shows it.
- Another dialog staging graph data (saved searches, layouts, ADR 0037's consequences) reuses these RPCs as they are:
  they name a namespace and a type, nothing of the LLM settings.
