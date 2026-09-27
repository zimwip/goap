# ADR 0014 — Node lifecycle, change attachment and documents

**Status**: accepted, implemented · **Date**: 2026-09

## Context

A node version carried only properties. Nothing said whether a node could be modified: any node of a
change's baseline could be proposed for update, and the import helpers (`CreateNode`, `UpdateNode`,
`Link`) wrote anywhere. Free-form `status` properties (`Requirement.status`, `Release.status`) carried
the intent without any rule. Compared with plm-core, aifact and ailm, the missing pieces were the same:
**state on the version**, a **declarative editable/frozen flag**, a **mandatory change** for writes and
**validation of what a document embeds**.

## Decision

1. **A domain is composed of node types, link types and lifecycles.** A lifecycle is a named,
   reusable state machine of the domain (`Schema.lifecycles`, `pkg/domain/lifecycle.go`): an initial
   state, states (`editable`, `final`) and transitions (`from`, `to`, `permission`, CEL `guard`,
   `requires` attributes / outgoing links, `children.states`). A node type **names** its lifecycle
   (`NodeType.lifecycle`); a subtype inherits it through `extends` and may name another one (it then
   replaces the inherited one). Lifecycles are validated when the domain is saved (unique names,
   unique states, known targets, an editable and a non-editable state, every editable state can reach a
   non-editable one, guards compile, referenced names exist). Types without a lifecycle behave as
   before. `changeControlled: false` opts a type out of change control (it cannot have a lifecycle).
   Stored with the domain version in the registry's database (ADR 0023).
2. **State lives on the node version** (`node_version.state`), so a baseline reproduces the states.
   The type catalogue (ADR 0012 §2, the graph's copy of the registry's model) resolves the lifecycle of each type,
   with `document` and `changeControlled`; a change is judged by the catalogue in force when it is checked.
3. **Editable is a working state inside a change.** A node is modified (properties, links, retirement) only when
   its effective state is editable. Persisted versions are never editable: a change **reopens** the node by writing
   a version in an editable state, edits it, and must move it to a non-editable state before it is applied. The
   effective state is the state of the latest version the change wrote. Change impacts (ADR 0024) apply the rules when a version is written, and `Apply` again for authority. Nodes with no state yet (created before the lifecycle) are unmanaged: editable, no
   leftover check.
4. **Attachment.** A change is attached to the nodes it modifies through its change impacts (ADR 0024;
   `GetChangeImpacts`, `ListNodeChanges`). **Several open changes may be attached to the same node**:
   conflicts are detected when they are applied (3-way merge, ADR 0009). The change stays an unversioned entity; its status now follows a machine
   (`draft → active → (merge_pending →) applied | abandoned`, only `Apply` applies; ADR 0015 §5).
5. **Transitions are checked when the change is applied**, for the actor who applies it: the
   transition exists, the actor holds its permission (`type:action`, default `node:transition`;
   callers without identity are trusted internal services), the node has the required attributes
   and links, the CEL guard holds. A node created with `create_node` starts in the initial state
   (or in `state` when a transition `initial → state` exists). A created node is the change's working copy: it may be
   written again in its initial state (ADR 0024).
6. **Documents.** A type with `document.contains` embeds nodes through outgoing `<namespace>@contains` links (`alm@contains`). The
   document gets a new version when its child set changes (outgoing links belong to the source
   version). A document transition with `children.states` is **validated, not cascaded**: every
   contained child, **in the result baseline** (so children moved by the same change count with
   their new state), must be in one of the states.
7. **Direct writes are closed** for lifecycle types: `CreateNode`, `UpdateNode` and `CreateLink`
   RPCs refuse them (`FailedPrecondition`); in-process seeds and imports keep the Go API.
8. **Blackboard.** Node views carry `state` and `editable` in CEL (`node.state`, `node.editable`); the DSL writes a
   state with `ctx.writeNode(node, {state})` (docs/dsl.md).

## Consequences

- A lifecycle is opt-in per type (`domains/alm.yaml`: `Requirement` family, `Release`).
- Actions that update approved nodes must reopen them first (llm actions are told so).
- Default policy `node:transition` (contributor / methodologist / approver of the org).
- Not done: refusing to publish a domain that removes a state still used by nodes (the registry does
  not query node states), a change-scoped read of the effective state in the UI, cascading
  transitions, suspect-link review workflow on state changes.
