# ADR 0076 — Checkout, working versions and check-in: every node write is a change operation

**Status**: accepted, implemented (graph, service, engine, web), then reshaped: the operation names follow ADR 0077; the
storage of the working version in `node_version` (`checked_out`, in-place edits of a version) is superseded by
[ADR 0079](0079-drafts-and-versions-at-landing.md) (a draft folded from the change log, the version written at landing);
the check-in (§5, `ImpactNodeCheckin`) by [ADR 0077](0077-impact-node-operations.md); §4d (editable initial state) by
ADR 0078; the review, rework and withdrawal rules of §5 / §5b amended by ADR 0102 · **Date**: 2026-10 · Refines ADR 0024
(change impacts), 0029 (event-sourced impacts), 0003 (version-to-version links), 0049 / 0054 (no write outside a
change). Supersedes `CreateObject`, the change-less node and link writes and `WriteChangeImpact`.

## Context

Rule 2 of the project says every modification is a change, and the guard (ADR 0054) checks that every version names
one. The API still offered three ways around the intent of that rule:

- The node writes took no change named by the caller: the graph wrapped them in a change of its own
  (`By: "graph.import"`, applied at once), so the write was journaled but no one stated why, under which methodology or
  for which unit; the link write named a change but checked nothing about the source node.
- `CreateObject` created a node through a change applied on main in the same call: the change existed only to satisfy
  the guard.
- `WriteChangeImpact` wrote a **new immutable version at every call**: an agent or a person editing a node property
  by property left a chain of intermediate versions on the change branch, each a `written` event, none of which
  meant anything to a reviewer. Links were edited by writing yet another version.

What a person expects from editing a controlled object is the checkout / check-in of a PLM: take the object into a
change, work on it as long as needed, have the result reviewed, freeze it.

## Decision

The names below are those of ADR 0077 (`ImpactNode<Verb>`, `ImpactLink<Verb>`, `ProposeImpact`, `WithdrawImpact`).

**Original storage, since replaced.** This ADR first kept the working state of a node in a **working version**: a row of
`node_version` flagged `checked_out`, written by the creation or the checkout, edited in place by the updates, the link
edits and the transitions, and frozen by a check-in (§5), later by the landing (ADR 0077). ADR 0079 replaced it: during a
change a node has **no version**, only a **draft** per change impact and flow, folded from the change log; the version
is written when the change lands (`CommitChange`). The sections below say "draft" for that working state.

### 1. Every node write names a change

`ImpactNodeCreate`, `ImpactNodeCheckout`, `ImpactNodeUpdate` and the link operations take a `change_id` (and the usual
optional `flow`, `execution`). There is no write of a node outside a change: the change-less writes, `CreateObject` and
the `graph.import` path are removed. An import is a change the importer creates first (`CreateChange`), then fills with
`ImpactNodeCreate` / `ImpactNodeCheckout`, reviews and applies. `CommitEdits` (`Graph.Commit`) stays as the composite of
those primitives for producers that do it all in one call (seeds, the settings dialog, `EnsureUser`).

### 2. A draft is created by `ImpactNodeCreate` or `ImpactNodeCheckout` (structural enforcement)

- **`ImpactNodeCreate(change, key, type, props, state?, owner?, rationale)`** creates the node: one `created` event adds
  the change impact `intent: created` and the draft of the node (on the flow named, or the active option). A new node is
  never proposed (ADR 0077).
- **`ImpactNodeCheckout(change, node, rationale)`** puts an existing node in edit mode: it declares the change impact
  `intent: modified` (or reuses the one proposed earlier by `ProposeImpact`) and gives it a draft copied from its base:
  properties, state, owner and outgoing links.
- A node has one draft per change and flow, from its creation or checkout until the change lands; a version, once
  written, is immutable. A second `ImpactNodeCheckout` on that flow is refused (`ErrConflict`, "already checked out").
  Two open changes may check out the same node: each works on its own branch and the integration rebases or merges them
  (ADR 0101, 0102).
- `ProposeImpact` stays for the impacts of existing nodes that are declared and not (yet) edited: an impact analysis
  ("this node is impacted, here is why", ADR 0024 §2) and the cross-namespace read impacts. Such an impact has no draft
  until an `ImpactNodeCheckout` realizes it.

### 3. `ImpactNodeUpdate` edits the draft

- **`ImpactNodeUpdate(change, impact, props)`** applies only to a node with a draft in the change; it merges the
  properties into it and writes **no version**. Refused on a node that is not checked out in the change.
- A draft is work in progress: every edit checks the **type and enum** of the values against the attributes of the
  node type (and of the link type for a link's properties), nothing more. What makes a version valid — the property
  and node validators of its type, the links its type requires (`requires:`, the parent of a node of a structure) and
  the attributes of its links — is checked when its review is **accepted** (the gate of the acceptance) and again when
  it **lands** (`CommitChange`, which writes the version), whatever operations built it. `CommitEdits` keeps its early
  check of the required parent of a creation, and a commit that fails leaves no change behind.
- `ImpactNodeUpdate` never changes the lifecycle state (§4b).
- **There is no retire.** A node is not removed by a change (ADR 0024 §4): removing a child is a modification of its
  **parent**, which is checked out and loses the link to the child (`ImpactLinkDelete`, §4). An impact analysis that
  says "X goes away" declares an impact on X (`ProposeImpact`) with `via` = the parent's impact; X is not checked out
  and keeps its versions. The `retire` option of `WriteChangeImpact` goes with it.
- The owner transfer (`NodeUpdate.Owner`) is an edit of the draft.

### 4b. A lifecycle transition

- **`ImpactNodeTransition(change, node, transition)`** moves a node along its lifecycle: it sets the state of the draft
  (a `transitioned` event with the patch `{"state": {"from", "to"}}`); a node with no draft is checked out first (ADR
  0079). When the change holds no impact on the node yet, it declares one (`intent: modified`). (This ADR first wrote a
  version per transition, frozen at once; ADR 0077 took the move in place on a working version, ADR 0079 on the draft.)
- Its control is the transition's own: the permission it declares (`Graph.Authorizer`, by default `node:transition`,
  asked before the transaction) and its guard.
- **A transition may require a review.** The review is carried by the change (the review of the node's impact, with
  its history, ADR 0024) and checked by the transition's guard, which sees the change impacts of the change: a guard
  such as "the impact of this node is accepted" refuses the transition until the review is there. Which transitions
  require it is the domain's lifecycle, not the platform's.
- A node guard saw the node, its children and the change header only (`pkg/graph/lifecycle.go`, `dsl.GuardCtx`). It
  gains the impact of the node in the change: CEL `impact` (`{intent, review, reviews, pre, post}`) and
  `GuardCtx.Impact()` for guard algorithms (ADR 0079 adds `draft`).
- The typical sequence: `ImpactNodeCheckout` → `ImpactNodeUpdate`… → `ImpactNodeTransition` → review → landing.

### 4. Links are edited on the draft

`ImpactLinkCreate`, `ImpactLinkUpdate` (properties) and `ImpactLinkDelete` apply only to an outgoing link of a draft of
the change; they change the draft's links **without writing a version**. The rule of ADR 0003 stays: the outgoing links
belong to the source version, and they are written with it when the change lands. A link to a node checked out in the
same change targets its draft, resolved at landing to the version the landing writes for it (ADR 0079). The links of a
version the change does not write stay where they are: they become suspect.

### 4c. What has no parent is retired by its lifecycle

A node that no parent holds is taken out of force by a transition, never deleted: the lifecycle `config` (`active`,
`retired`; transitions `retire` and `restore`) is given to the entries of the platform configuration (`MCP`,
`AdapterDef`, `LlmProvider`, `LlmModel`, `LlmAlias`) and to the access and adapter entries of the organisation
(`Adapter`, `Policy`, `CriticalityPolicy`, `Assignment`). Their readers leave a retired node out: the model gateway
(`llmcfg.StateRetired`), the access and hub snapshots (`access.InForce`). A user is deactivated (lifecycle `user`).
`CommitEdits` carries a `state` per edit: it retires a node, and restores a retired one in the same change as its other
edits.

### 4d. A domain lifecycle does not repeat the review of the change

This section first required the initial state of a lifecycle to be **editable**, since a node was created checked out
in it; ADR 0078 removed the `editable` flag (edits are allowed in any state, a state may opt out of landing). A node
that rests in a state no one works in (`proposed`, `candidate`) is moved there by a transition. The ALM lifecycles
start in `draft` (`requirement` gains `propose`, `draft` → `proposed`; `release` and `maturity` already lead out of
`draft`).

The review is the change's: a lifecycle state "under review" would come after the review that gates the landing.
The `requirement` lifecycle of the ALM domain loses `in_review`, `submit` and `reject`: `approve` goes from `draft` to
`approved`, its guard requires the accepted review of the impact (`impact.review == "accepted"`); a refused review
sends the requirement back to be reworked in the change. The default policies grant `requirement:approve` to the
members of the project: the control is the review. The SDLC methodology approves the accepted requirements after its
review (`approve_requirements`, before `apply_change`).

### 5. Review

- The review judges the draft as it stands. This section first had a check-in (`ImpactNodeCheckin`) that froze an
  accepted working version; ADR 0077 removed it. An accepted review runs the checks of a version (validators, required
  links, link attributes, the origins gate) and refuses the acceptance when one fails; it writes nothing else. Since ADR
  0102 only a **modification** is accepted (a draft that differs from its base, a creation, a merge), and since ADR 0105
  every verdict is made by a submitted review (ADR 0080).
- Editing a draft after a decided review (`ImpactNodeUpdate`, the link operations) or cancelling it sends the review
  back to `proposed`; a checkout and a lifecycle move leave it.
- A **rejected** impact keeps its draft, to be reworked (an edit sends it back to `proposed`, ADR 0102) or withdrawn
  (§5b); `ReopenChangeImpacts` sends an accepted or a rejected impact back to `proposed`.
- `CommitChange` writes the accepted modifications (ADR 0102); the merge of the change needs every impact settled (none
  rejected or merely proposed). `AdoptFlow` refuses a flow draft its flow did not accept, `DiscardFlow` drops them.

### 5b. Cancelling a checkout, withdrawing an impact

- **`ImpactNodeCancel(change, impact)`** drops the draft of an impact on its flow: the impact goes back to what the
  flow saw before (the draft of a parent flow, the version the change committed, else none).
- **A creation cancelled** leaves no node behind: the impact goes with it and its key is free. No node row was ever
  written (ADR 0079); a removal is a modification of the parent (§3).
- **`WithdrawImpact(change, impact)`** takes an impact out of the change, explicitly: its draft, if any, is dropped and
  the impact leaves the list (`withdrawn` event), with the impacts `via` it. This ADR first refused it once a version
  of the impact was frozen, for an impact derived from items and for one realizing another impact's removal; since ADR
  0102 it is possible at any time while the change is open, and a node the change committed is reverted on its branch
  by the next commit. Only the flow that declared an impact withdraws it.

### 6. Events

The impact log (ADR 0029) records `proposed`, `created` (a creation, in one event: the change impact and the draft of
the node, ADR 0077), `checkedOut` (the draft a checkout gives an existing node), `transitioned` (a move of the draft, a
merge or an adoption), `updated` (the property and link edits, with the patch, for the audit trail and PROV-O),
`cancelled` (a checkout dropped), `withdrawn` (an impact taken out of the change), `reviewed`, `discarded`, `adopted`,
`landed`, `rebased`. The node index (ADR 0026) is fed when a version is written (at landing): a `NodeEvent` is
published for a written version, never for an edit of a draft.

### 7. API

| Removed | Replaced by |
| --- | --- |
| change-less node creation, `CreateObject` | `ImpactNodeCreate(change, …)` (ABAC `object:create` on the change's project, `gateAccess` for `adminOnly` types) |
| change-less node update | `ImpactNodeCheckout` then `ImpactNodeUpdate(change, impact, props)` |
| link write with an optional change | `ImpactLinkCreate` / `ImpactLinkUpdate` / `ImpactLinkDelete` on a checked-out source |
| `WriteChangeImpact` | `ImpactNodeUpdate` (props, owner), the link operations (a removal: the parent loses its link) and `ImpactNodeTransition` (state) |
| — | `ImpactNodeTransition(change, node, transition)` |
| — | `ImpactNodeCancel(change, impact)` |
| — | `WithdrawImpact(change, impact)` |

`gateAccess` (the `adminOnly` types, ADR 0068) applies to `ImpactNodeCreate` and `ImpactNodeCheckout`; every later
operation on the impact inherits the check.

## Consequences

- Rule 2 holds structurally: no RPC writes a node without naming a change, and no version exists without the change
  that wrote it.
- The history of a node has one version per change that landed it (ADR 0079), instead of one per edit; a reviewer
  reviews what lands.
- `pkg/graph`: `NodeWrite` split into the checkout, update, link and transition operations; `CreateObject` and the
  `graph.import` commit went; `Commit` is rebuilt on the primitives.
- Callers migrated: `internal/graphsvc` (handler, client), `pkg/dsl` / `pkg/engine` node operations, the `goap-change`
  built-in MCP tools, the web, the seeds and the tests that created nodes directly.
