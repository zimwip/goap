# ADR 0083 — The impact diff between two flows

**Status**: accepted, implemented (graph, services, web) · **Date**: 2026-10 · Refines ADR 0032 §6 (comparison of options),
follows ADR 0079 (drafts), names after ADR 0077.

## Context

`CompareOptions` (ADR 0032) answered "which nodes did the open options change from the main flow", from the versions each
flow's view holds, then the properties. With drafts (ADR 0079) a node of a change has no version and the options are
compared to the main flow only, as a table of every property of every side, with the identical values next to the
different ones. The Compare tab was unusable: the person could not choose what to compare with, the impacts that did not
differ were as present as the ones that did, and nothing said whether an impact was added, removed or modified.

## Decision

- **One operation**: `Graph.DiffFlows(change, left, right, level)` (RPC `DiffFlows`, `graphsvc.Client.DiffFlows`). A flow
  is `main` (or empty) or any option of the change, open or decided; two options are compared as well as an option with
  the main flow; the same flow on both sides yields only identical impacts. The level keeps the meaning it had: `written`
  is every impact the flow sees but the rejected ones, `accepted` the impacts accepted on that flow.
- **What a flow sees**: the impacts of the flow (`flowNodes.nodes`: its own and those of its chain, the review and
  supersession as the flow resolves them) and, for each, the draft the flow reads of its node (`draftReader`): properties,
  lifecycle state, owner and outgoing links. A planned impact (no draft yet) reads the version it starts from; a planned
  creation, nothing but its key and type.
- **Matching and categories**: impacts are matched by node id, then, for a node created apart on both flows, by type and
  key. *Added*: the right flow only; *removed*: the left only; *modified*: both, with a different content; identical
  impacts are not listed, only counted (`FlowDiff.Identical`). A different review alone is not a difference of content (the
  review of each side is on the row).
- **Attribute level**: `domain.DiffShapes(left, right)`, a pure function over two `NodeShape`s, lists the `FieldChange`s
  (`kind` property / state / owner / link, `op` added / removed / changed, old and new values). A link is its type and its
  target node (a draft points at the draft of its target; the key names it); the same link with other properties is a
  change. Values are compared as JSON.
- **Web**: the comparison is not a tab but the **Compare & decide dialog** (`CompareDialog.svelte`), opened from the scope
  bar when the change has options: a left and a right scope (main flow and the option the scope bar looks at, else the
  first open option), a swap, the level, filter chips with the counts, the rows grouped by category with the changes
  (long values truncated, expandable), the count of identical impacts, a link from each row to the impact in the Impacts
  pane of its scope, and the decision of the right-hand option (evaluate, select, reject). The pure logic (defaults,
  grouping, filters, wording) is `web/src/lib/flowDiff.ts`.
- `CompareOptions` stays for the `goap-change` tool (`compare`); the web no longer calls it.

## Consequences

- The diff reads drafts only: after landing the drafts are gone and the versions are the history (ADR 0079); comparing a
  landed change is a baseline comparison.
- A rejected impact is not part of a flow at `written` (as in `ChangeView`).
- Not done: the diff of a sub-change does not show the drafts inherited from its parent as impacts of their own.
