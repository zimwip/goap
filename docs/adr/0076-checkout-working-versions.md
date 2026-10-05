# ADR 0076 — Checkout, working versions and check-in: every node write is a change operation

**Status**: proposed · **Date**: 2026-10 · Refines ADR 0024 (change impacts), 0029 (event-sourced impacts), 0003
(version-to-version links), 0049 / 0054 (no write outside a change). Supersedes `CreateObject`, the direct
`CreateNode` / `UpdateNode` / `CreateLink` RPCs and `WriteChangeImpact`.

## Context

Rule 2 of the project says every modification is a change, and the guard (ADR 0054) checks that every version names
one. The API still offers three ways around the intent of that rule:

- `CreateNode` and `UpdateNode` write a node with no change named by the caller: the graph wraps them in a change of
  its own (`By: "graph.import"`, applied at once), so the write is journaled but no one stated why, under which
  methodology or for which unit; `CreateLink` names a change but checks nothing about the source node.
- `CreateObject` creates a node through a change applied on main in the same call: the change exists only to satisfy
  the guard.
- `WriteChangeImpact` writes a **new immutable version at every call**: an agent or a person editing a node property
  by property leaves a chain of intermediate versions on the change branch, each a `written` event, none of which
  means anything to a reviewer. Links are edited by writing yet another version.

What a person expects from editing a controlled object is the checkout / check-in of a PLM: take the object into a
change, work on it as long as needed, have the result reviewed, freeze it.

## Decision

### 1. Every node write names a change

`CreateNode`, `CheckoutNode`, `UpdateNode`, `CheckinNode` and the link operations take a `change_id` (and the usual
optional `flow`, `execution`). There is no write of a node outside a change: the change-less `CreateNode` /
`UpdateNode`, `CreateObject` and the `graph.import` path are removed. An import is a change the importer creates first
(`CreateChange`), then fills with `CreateNode` / `CheckoutNode`, reviews, checks in and applies. `CommitEdits` stays as
the composite of those primitives for producers that do it all in one call (seeds, the settings dialog, `EnsureUser`).

### 2. A working version is created by `CreateNode` or `CheckoutNode` (structural enforcement)

- **`CreateNode(change, key, type, props, state?, owner?, rationale)`** declares a change impact `intent: created` and
  writes version 1 of the node (`reason: create`) on the change branch (the flow branch when a flow is named or an
  option is active). The version is the impact's `post` and is **checked out**.
- **`CheckoutNode(change, node, rationale)`** puts an existing node in edit mode: it declares the change impact
  `intent: modified` (or realizes one declared earlier by `AddChangeImpacts`), and writes the next version
  (`reason: revise`, `parents: [pre]`, or the last checked-in version of the impact) as a copy of its base: properties,
  state, owner and outgoing links. That version is the impact's `post` and is checked out.
- A version is **checked out** (mutable) from its creation by one of these two calls until its check-in; every other
  version is immutable, as today. `node_version` gets a `checked_out` flag; the guard refuses any update of a version
  that is not checked out by the change of the transaction.
- `CheckoutNode` on an impact that is already checked out on that flow is refused (`ErrConflict`, "already checked
  out"). Two open changes may check out the same node: each works on its own branch and the integration merges them
  (`GetSharedNodes`, `MergeChange`), as today.
- `AddChangeImpacts` stays for the impacts that are declared and not (yet) edited: an impact analysis ("this node is
  impacted, here is why", ADR 0024 §2) and the cross-namespace read impacts. Such an impact has no version until a
  `CheckoutNode` realizes it.

### 3. `UpdateNode` edits the working version in place

- **`UpdateNode(change, impact, props)`** applies only to a checked-out version of the change; it merges the
  properties into it (the semantics of `NodeWrite.Properties` today) and writes **no new version**. Refused on a node
  that is not checked out in the change, or checked in.
- **A change of lifecycle state creates a new version.** `UpdateNode` with a `state` freezes the current working
  version (it stays in the history, unreviewed) and writes the next version in the new state, which becomes the
  working version: the node stays checked out. The transition is authorized at that call (`Graph.Authorizer`), before
  the transaction. Retiring a node is the same: the new version is a tombstone, the last working version of the
  impact.
- The owner transfer (`NodeWrite.Owner`) is an in-place edit of the working version.

### 4. Links are edited in place on the working version

`CreateLink`, `UpdateLink` (properties) and `DeleteLink` apply only to an outgoing link of a checked-out version of the
change; they change that version's links **without writing a new version**. The rule of ADR 0003 stays: the outgoing
links belong to the source version, and they are frozen with it at check-in. A link to a node checked out in the same
change targets its working version, whose number does not move until the next checkout.

### 5. Review and check-in

- `ReviewChangeImpact` reviews the working version as it stands. **An accepted review authorizes the check-in**; it is
  not a check-in itself.
- **`CheckinNode(change, impact)`** freezes the working version: refused unless the impact's review is `accepted`
  on that flow. After it, `UpdateNode` and the link operations are refused on that version.
- To modify a checked-in node again, the change makes a **new `CheckoutNode`**: it writes the next version (parent:
  the checked-in one), and the impact's review goes back to `proposed`. A rejected impact keeps today's rule: it is
  not written until it is reopened (`ReopenChangeImpacts`).
- `CommitChange` (so `Apply`) requires every version of the change to be checked in, in place of today's "accepted
  impacts await their review" check; `AdoptFlow` refuses a flow that still has a checked-out version, `DiscardFlow`
  drops them.

### 6. Events

The impact log (ADR 0029) records `declared`, `checkedOut` (the working version, `Post`, replaces `written`),
`updated` (the property and link edits made in place, with the patch, for the audit trail and PROV-O), `checkedIn`,
`reviewed`, `discarded`, `adopted`, `landed`, `rebased`. The node index (ADR 0026) is fed at check-in: a
`NodeEvent` is published for a frozen version, never for each in-place edit.

### 7. API

| Removed | Replaced by |
| --- | --- |
| `CreateNode` without change, `CreateObject` | `CreateNode(change, …)` (ABAC `object:create` on the change's project, `gateAccess` for `adminOnly` types) |
| `UpdateNode(ref, props)` | `CheckoutNode` then `UpdateNode(change, impact, props)` |
| `CreateLink(change?, from, to)` | `CreateLink` / `UpdateLink` / `DeleteLink` on a checked-out source |
| `WriteChangeImpact` | `UpdateNode` (props, state, retire, owner) and the link operations |
| — | `CheckinNode(change, impact)` |

`gateAccess` (the `adminOnly` types, ADR 0068) applies to `CreateNode` and `CheckoutNode`; every later operation on the
impact inherits the check.

## Consequences

- Rule 2 holds structurally: no RPC writes a node without naming a change, and no version exists without the change
  that checked it out.
- The history of a node has one version per checkout and per state change, instead of one per edit; a reviewer reviews
  the version that lands.
- A version is no longer immutable from its creation but from its check-in: code that caches a version by its ref
  (the guard's replay, the index) must read checked-in versions only, or be told of in-place edits (`updated`).
- `pkg/graph`: `NodeWrite` splits into the checkout, update, link and check-in operations; `Graph.CreateNode(NewNode)`,
  `UpdateNode(ref)`, `Link`, `CreateObject` and the `graph.import` commit go; `Commit` is rebuilt on the primitives.
  Schema: `node_version.checked_out` in both dialects (`TestSchemasAligned`).
- Callers to migrate: `internal/graphsvc` (handler, client), `pkg/dsl` / `pkg/engine` node operations, the
  `goap-change` built-in MCP tools, the web (`ChangeTab`, `ObjectDialog`, `pending.svelte.ts`, `llmEdit.ts`,
  `lifecycle.ts`, `HumanTaskForm`), the seeds and the tests that create nodes directly.

## Plan

1. Domain and storage: the `checked_out` flag, the guard rule, in-place edits of a working version and of its links,
   the new impact operations.
2. `pkg/graph`: `CreateNode`, `CheckoutNode`, `UpdateNode`, the link operations, `CheckinNode`; the check-in
   precondition of `CommitChange`, `AdoptFlow`; removal of the direct writes; `Commit` on the primitives.
3. Proto, handler, client, authorization.
4. Engine, DSL, built-in MCP tools.
5. Web.
6. `docs/architecture.md`, `docs/dsl.md`, `CLAUDE.md`; tests under `GOAP_MATERIALIZE_EVERY=1`, `3`, `100000` and
   `make test-pg`.
