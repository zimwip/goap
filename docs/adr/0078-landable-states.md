# ADR 0078 — Landable states replace the editable flag

**Status**: accepted, implemented · **Date**: 2026-10 · Supersedes in part ADR 0014 §1 and §3 (the `editable` state
flag, reopening a node before editing it), ADR 0048 (`RestInEditable`) and ADR 0076 §4d (the initial state of a
lifecycle must be editable).

## Context

A lifecycle state carried `editable`: a node was modified only in an editable state, a change had to reopen a node by a
transition before editing it, and it could not land while a node rested in an editable state, unless the lifecycle
declared `restInEditable` (ADR 0048) because its editable states were ordinary long-lived statuses. Three rules
(edit only when editable, land only when not editable, but not when `restInEditable`) came from one flag with two
meanings: a *work in progress* mark and a *may be edited* mark. `User`, `config` and the other resting lifecycles
needed the exception; the checkout of a node at rest refused with "reopen it first", and `CommitEdits` had to take a
transition first to restore a retired entry.

## Decision

1. **Edits are allowed in any state** while the node is in a change: `ImpactNodeCheckout`, `ImpactNodeUpdate`, the link
   operations and `CommitEdits` never look at the state. There is no "reopen first" step. The state only says what a
   node may do next (its transitions) and whether it may land.
2. **A state may opt out of landing**: `LifecycleState.NotLandable` (`notLandable: true` in YAML and JSON,
   `not_landable` in the registry proto). Every state is landable by default. `Lifecycle.Landable(state)` is true
   for no lifecycle, no state (`""`) and a state the lifecycle does not know, else `!NotLandable`.
3. **Only a node whose state is landable can land.** `Apply` / `CommitChange` (`walkChangeImpacts`,
   `settleChangeImpacts`) and the merge of a branch refuse when a node of the change ends in a state that is not
   landable, naming each node: "the change leaves nodes in a state that cannot land, move them to a landable state
   first: KEY (type) in state, ...". The refusal is unconditional (no `RestInEditable`); a landing gate that decides
   (`Graph.LandingGate`, ADR 0066) replaces it, as it replaced the editable floor.
4. **Lifecycle validation** (`Lifecycle.Issues`): a state flagged `notLandable` must be able to reach a landable
   state (else a node there could never land), a final state is not `notLandable` (it has no exit), and at least
   one state is landable (automatic unless all are flagged). The initial state may be flagged: a created node is born in
   it and the change moves it on. The rules "initial state editable", "an editable and a non-editable state" and
   "every editable state reaches a non-editable one" are gone.
5. **`CommitEdits`**: a node edited and given a `state` is edited, then moved by a transition of its own in the same
   change; a retired node is simply edited and restored, with no move first (`reopens` is gone).
6. **Blackboard**: `NodeView.Frozen` ("the state is not editable") becomes `NodeView.NotLandable` (proto
   `NodeView.not_landable`), and the CEL variable `editable` of a node becomes `landable`. It is derived from the state
   of the version the change leaves, not from whether the version is frozen: a working or a checked-in version is
   frozen by the guard (ADR 0076), which is unchanged.

Migration of the domains: only the states that were editable and not covered by `restInEditable` keep a meaning, and
become `notLandable: true` (the `draft` of `alm`'s `requirement`, `release` and `maturity`). The `active` / `proposed`
states of `organisation` and `platform` that were editable at rest are ordinary landable states; `retired` and
`deactivated` are no longer read-only.

## Consequences

- No lifecycle needs an exception to rest in a state: a status is landable by default, a draft opts out.
- A node is edited wherever it is; the web editors no longer disable Edit or offer "Reopen" buttons, and the change view
  lists the nodes in a state that cannot land ("not landable" tag, `ChangeTab` blocks Apply).
- The guard's own `editable` check (a version written by a past transaction, not a working version, cannot be edited)
  is unchanged, and unrelated to the lifecycle.
- Existing user-defined domains that declared `editable` or `restInEditable` must be republished (greenfield: no
  migration, no alias).
