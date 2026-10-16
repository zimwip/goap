# ADR 0014 — Node lifecycle, change attachment and documents

**Status**: accepted, implemented · **Date**: 2026-09 · §1 and §3 (the `editable` flag, reopening) superseded by
ADR 0078; §3, §5 and §7 (versions written in a change, transitions checked at apply, direct writes) superseded by
ADR 0049, 0076 and 0079 (every write in a change, drafts, versions written at landing); §4's status machine amended by
ADR 0102

## Context

A node version carried only properties. Nothing said whether a node could be modified: any node of a
change's baseline could be proposed for update, and the import helpers (`CreateNode`, `UpdateNode`,
`Link`) wrote anywhere. Free-form `status` properties (`Requirement.status`, `Release.status`) carried
the intent without any rule. Compared with plm-core, aifact and ailm, the missing pieces were the same:
**state on the version**, a **declarative editable/frozen flag**, a **mandatory change** for writes and
**validation of what a document embeds**.

## Decision

1. **A domain is composed of node types, link types and lifecycles.** A lifecycle is a named,
   reusable state machine of the domain (`Schema.lifecycles`, `contracts/domain/lifecycle.go`): an initial
   state, states (`final`; first `editable`, now `notLandable`, ADR 0078) and transitions (`from`, `to`,
   `permission`, CEL `guard`, `requires` attributes / outgoing links, `children.states`). A node type **names** its
   lifecycle (`NodeType.lifecycle`); a subtype inherits it through `extends` and may name another one (it then
   replaces the inherited one). Lifecycles are validated when the domain is saved (unique names, unique states, known
   targets, guards compile, referenced names exist, and the landing rules of ADR 0078: a `notLandable` state reaches a
   landable one). Types without a lifecycle behave as before. `changeControlled: false` opts a type out of change
   control (it cannot have a lifecycle). Stored with the domain version in the registry's database (ADR 0023).
2. **State lives on the node version** (`node_version.state`), so a baseline reproduces the states.
   The type catalogue (ADR 0012 §2, the graph's copy of the registry's model) resolves the lifecycle of each type,
   with `document` and `changeControlled`; a change is judged by the catalogue in force when it is checked.
3. **Editable is a working state inside a change** (original decision). A node was modified (properties, links,
   retirement) only when its effective state was editable: a change **reopened** the node by writing a version in an
   editable state, edited it, and had to move it to a non-editable state before it was applied. *Since then*: a node
   has no version while a change works on it, only a draft carrying its state (ADR 0079); edits are allowed in any
   state, and a change lands only when each node ends in a landable state (ADR 0078).
4. **Attachment.** A change is attached to the nodes it modifies through its change impacts (ADR 0024;
   `GetChangeImpacts`, `ListNodeChanges`). **Several open changes may be attached to the same node**:
   conflicts are detected when they are applied (3-way merge, ADR 0009). The change stays an unversioned entity; its
   status follows a machine (`draft → active → (committed →) applied | abandoned`; `committed`, first `merge_pending`,
   is a merge waiting for its resolutions; only the merge applies, ADR 0102; ADR 0015 §5).
5. **Transitions are checked**: the transition exists, the actor holds its permission (`type:action`, default
   `node:transition`), the node has the required attributes and links, the CEL guard holds.
   They were first checked when the change was applied, for the actor who applied it; since ADR 0076 / 0079 they are
   checked when the move is taken on the draft (`ImpactNodeTransition`), and the landing replays the moves recorded in
   the change log (`walkTransitions`). A created node starts in the initial state; anonymous callers are no longer
   trusted (every graph RPC is identified).
6. **Documents.** A type with `document.contains` embeds nodes through outgoing `<namespace>@contains` links (`alm@contains`). The
   document gets a new version when its child set changes (outgoing links belong to the source
   version). A document transition with `children.states` is **validated, not cascaded**: every
   contained child, as the change sees it (so children moved by the same change count with
   their new state), must be in one of the states.
7. **Direct writes are closed** for lifecycle types (original decision: the `CreateNode`, `UpdateNode` and
   `CreateLink` RPCs refused them, seeds and imports kept the Go API). *Since then* there is no direct write at all:
   every node, version and link write names a change, seeds included (ADR 0049, 0054, 0076), and those RPCs are gone.
8. **Blackboard.** Node views carry `state` and `landable` (first `editable`, ADR 0078) in CEL (`node.state`,
   `node.landable`); the DSL moves a node with `ctx.impactNodeTransition(node, state)` (docs/dsl.md).

## Consequences

- A lifecycle is opt-in per type (`domains/alm.yaml`: `Requirement` family, `Release`).
- Default policy `node:transition`: any member of the project (`onProject`, ADR 0043; first contributor /
  methodologist / approver of the org).
- Not done: refusing to publish a domain that removes a state still used by nodes (the registry does
  not query node states), cascading transitions, suspect-link review workflow on state changes.
