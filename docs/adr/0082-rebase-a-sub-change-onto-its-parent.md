# ADR 0082 — A sub-change is rebased onto its parent; its integration is a fast-forward

**Status**: accepted, implemented (graph) · **Date**: 2026-10 · Amends ADR 0081 §5 (a conflict leaves the sub-change
committed until `IntegrateChange` gets a `Resolution`); builds on ADR 0079 (drafts folded from the log, reviews explicit,
an edit after an acceptance sends the review back to `proposed`) and ADR 0081 (a sub-change lands in its parent's log).

## Context

ADR 0081 integrates a sub-change into its parent by installing its drafts in the parent's log, and reads a conflict there:
the parent changed a node since the sub-change took it. The conflict was resolved at integration, by a `Resolution` handed to
`IntegrateChange`: `Skip` left the node out, `Props` installed properties given whole. Two weaknesses:

- the resolution was written into the parent without being reviewed: the properties a `Resolution` carried were accepted in
  the parent by the integration itself;
- a conflict was all or nothing per node: a parent that changed one property and a sub-change that changed another were in
  conflict, though nothing contradicted.

The sub-change is the work of the unit it was delegated to (ADR 0016): the parent's evolutions it builds on are its to take in,
and a contradiction its to resolve, under its own review. The log makes this exact: the state the sub-change started from is
the parent's draft at a known position of the parent's log (`Draft.Inherited.Seq`, ADR 0081 §2), which a fold rebuilds.

## Decision

### 1. Rebase

`Graph.RebaseChange(change)` brings an open or committed sub-change up to date with its parent. A draft of its main flow is
**behind** when the parent changed the node since the sub-change took it (the rule of ADR 0081 §5: an event changing the
parent's draft after `Inherited.Seq`, a parent draft of a node the sub-change took from the stored version, or the parent's
impact of the node rejected or gone). For each draft behind, three states are compared:

- **base**: what the sub-change started from: the draft of the change `Inherited` names, folded from that change's log up to
  `Inherited.Seq` (`domain.DraftSeenBy` over the events up to the seq); for a draft taken from the stored version, that version
  (`Draft.Base`, with its outgoing links);
- **theirs**: the parent's draft of the node now (else, the parent holding no draft, the base);
- **ours**: the sub-change's draft.

They are merged field by field (three-way): each property (set or unset), the owner, the state, and each outgoing link, a link
being identified by its type and its target node (its version and properties are its value). A field changed on one side only
takes that side's value; changed on both sides to the same value, that value; changed on both sides differently it is a
**conflict**: the draft keeps ours and the field is named. The parent having rejected or withdrawn its impact of the node is a
conflict on the whole node (`node`). Conflict names: `props.<key>`, `owner`, `state`, `link:<type>:<target node>`, `node`.

The merged draft is installed on the sub-change's main flow by a `transitioned` event carrying the `Draft`, with `Inherited` set
to the parent's draft and the seq of its last event, and `Patch {"rebased": {"change", "seq"}, "conflicts": [...]}`; the
lifecycle walk restarts from it (ADR 0079 §6), as for an adoption. When the merged draft differs from ours, what was accepted is
no longer what is there: the review goes back to `proposed` (a `reviewed` event, comment "rebased onto parent change …").
A draft whose merge equals ours keeps its review and only moves its `Inherited` position.

### 2. Resolution in the sub-change

The conflicts of an impact are those its last rebase named, less those an edit of the sub-change's draft settled since: an
edit of a property (set or unset) settles `props.<key>`, of the owner `owner`, a transition `state`, a link added, removed or
updated to a target node `link:<type>:<target>`, and any edit `node`. `Graph.ImpactNodeResolve(change, impact)` settles the ones
left by keeping the sub-change's values (an `updated` event, `Patch {"resolved": true}`). Taking the parent's version of a node
is cancelling the sub-change's draft (`ImpactNodeCancel`) or withdrawing its impact (`WithdrawImpact`). An impact with
conflicts not settled cannot be accepted (`ErrConflict` naming them); the resolution goes through the ordinary review.

### 3. Integration is a fast-forward

`IntegrateChange` of a sub-change installs its drafts in the parent (ADR 0081 §4) only when none is behind; else it is refused
(`ErrConflict`, "rebase onto the parent first") and nothing is written. `Resolution` no longer applies to a sub-change.

`Apply` of a sub-change rebases it first, in a transaction of its own: when nothing was behind, it commits and integrates in
one transaction as before; when the rebase changed a draft, the rebase stays and `Apply` stops with `ErrConflict`, naming the
impacts to review again and the conflicts to settle. A parent that moves between the rebase and the integration makes the
integration refuse; `Apply` again rebases again.

## Consequences

- Every value the parent receives from a sub-change was accepted by a review of the sub-change, conflicts included.
- Evolutions that do not contradict merge without a person; only a field changed on both sides asks one.
- The parent's log receives only clean integrations; the sub-change's log tells each rebase, the conflicts it named and how
  they were settled.
- The `removeLink` and `updateLink` patches of the `updated` event name the target node (`toId`, and the type for
  `updateLink`), so a link edit can settle a link conflict.
- Exposed by the RPCs `RebaseChange`, `ImpactNodeResolve` and `GetRebaseState` (`Graph.RebaseState`: the impacts behind the
  parent, the conflicts left), `graphsvc.Client` and the web (`SubChangeSync.svelte` on the change's overview: Rebase, the
  conflicts with "Keep mine"; `ApplyChange` rebases by itself).
- Not done: a `goap-change` tool for the rebase and the resolution (an agent reaches them through `apply` and its edits).
  Merging the values of a property (text, lists) is not attempted: a property is one value.
