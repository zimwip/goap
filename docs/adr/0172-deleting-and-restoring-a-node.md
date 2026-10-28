# ADR 0172 — Deleting a node, restoring it, and a creation of an existing node

**Status**: accepted, implemented · **Date**: 2026-10 · **Amends**: ADR 0024 §4, ADR 0076 (a node is never deleted),
ADR 0147 §4 (the `presence` lifecycle of the code domain)

## Context

Since ADR 0024 a node is never deleted: removing a child is a modification of its parent, and a node no parent holds is
retired by its lifecycle (`config`: `active` / `retired`, ADR 0076). The git import (ADR 0147) followed the rule with a
lifecycle of its own, `presence` (`present` / `removed`): an entry a commit removes stays in the head of the graph,
moved to `removed`, and comes back `restore`d when a commit adds it again.

For source code this is the wrong shape. A removed file or a removed declaration is not a state of the node the head
should keep: every reader of the head (a view, a read query, a search, a delta followed by the importer, the statistics)
had to leave the `removed` nodes out, and a tree where most nodes ever written are `removed` costs to read for nothing.
The storage already knew a deletion: a version may be a tombstone (`Node.Deleted`, ADR 0032), the state of a baseline
leaves out a node whose last commit deletes it (ADR 0143), and the revert of a withdrawn creation writes one (ADR 0140).
Only no operation of a change wrote one.

A second question comes with it. A change that creates a node whose key the graph holds already (alive, or deleted)
was refused (`ErrConflict`, "the reference baseline already holds it"). The work it does is an impact the change did not
identify upfront: the modification of that node, found after the fact.

## Decision

1. **A change deletes a node.** `Graph.ImpactNodeDelete(ctx, change, NodeCheckout)` (operation `impact.delete`, doer;
   RPC `ImpactNodeDelete`, `graphapi.Port`, `graphclient`) marks the draft of the node deleted (`Draft.Deleted`), the
   node checked out first when the flow holds no draft of it: an `updated` event with the patch `{"deleted": true,
   "removeLinks": [...]}`. A deletion is no new intent: the impact is `modified`. A deleted draft is not edited any
   more (`ErrConflict`); cancelling it takes the deletion back. A node the change creates is not deleted: its creation
   is cancelled (`ErrInvalid`).
2. **The commit writes a tombstone.** The version written from a deleted draft has `Deleted` set, keeps the last
   properties (what a search of the history finds, what a restoration starts from) and holds no outgoing link; it is
   accepted with no rule of a version (`checkDraft`). The node leaves the state of the branch the commit lands on,
   by the selection of ADR 0143 (`a.target.remove`). Links of other nodes to it stay, suspect: `SuspectLinks` now also
   returns a link whose target of the baseline's namespace the baseline no longer holds.
3. **A creation of an existing key is requalified.** When a change creates a node whose key names a node the reference
   baseline of the change holds, or one its last commit there deleted (`Graph.tombstone`: the node's last commit on
   the baseline's branch up to its order, then on the branch it was forked from; one indexed read per branch of the
   chain, never the history), or a node the change declared without a draft, the creation is the modification of that
   node: an impact `modified` whose pre is the version held (the tombstone for a deleted node), recorded as three
   separate entries of the log of the change: `proposed`, then **`requalified`** (`domain.ImpactRequalified`, Pre the
   version, patch `{"from": "created", "to": "modified", "key", "type", "restored"}`), then `checkedOut` (a deleted
   node checked out from its tombstone is restored: the draft is not deleted), then `updated` with what the creation
   gives (its properties set over the node's, its owner, the links it names that the node does not hold). A key of
   another type stays a conflict, and so does a node the change holds a draft of. A modification is never requalified:
   an update names an impact or a node the change checked out.
4. **A checkout of a deleted node restores it.** Naming a node the reference baseline deleted (its id, or its key) to
   check it out, or proposing its tombstone as the pre of an impact (`ProposeImpact`), declares the modification of
   that node from its tombstone; the draft is the node as it was deleted, not deleted. A restoration is a modification
   (`DraftChanged` compares `Deleted`).
5. **Rebase and merge keep a deletion** (`MergeDrafts`: a deletion on either side holds; `DraftDiff` carries
   `deleted`).
6. **The code domain deletes** (`code` 1.7.0): the `presence` lifecycle is gone. The git importer deletes the entries
   and symbols a commit removes (`ImpactNodeDelete`, no link to drop), follows the deleted nodes of a delta
   (`BaselineDelta.Removed`, the entry marked removed in its index), and creates an entry added again, which the graph
   requalifies and restores: the same node.

## Consequences

- The heads, views, read queries, deltas and statistics of a code graph hold only what the last commit imported holds;
  the history keeps every version, tombstones included (the index flags them `deleted`).
- The web's audit trail names the `requalified` entry; the PROV-O export maps it to `goap:requalifiedOn`.
- A search of deleted nodes to select them as impacts was considered and left out: the creation of their key (or a
  checkout by id or key) is the way back, checked at the baseline of the change.
- Not done: a `delete` tool of `goap-change`, a DSL call and a button of the change tab; a deleted node in the
  explorers of a baseline.
