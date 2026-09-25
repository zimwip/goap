# ADR 0014 — Node lifecycle, change attachment and documents

**Status**: accepted · **Date**: 2026-09

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
   Stored: registry proto `Domain.lifecycles` / `Methodology.lifecycles`, tables `domain_lifecycle` and
   `methodology_lifecycle` (PostgreSQL), the definition JSON (SQLite).
2. **State lives on the node version** (`node_version.state`), so a baseline reproduces the states.
   The graph holds no Lifecycle nodes: the NodeType node embeds the **resolved** lifecycle it names
   (`lifecycle`, and its name in `lifecycleRef`), so evaluating a change needs no other lookup.
   `Sync` patches `lifecycle`, `lifecycleRef`, `document` and `changeControlled` of existing NodeType
   nodes (the rest of a NodeType stays graph-native, ADR 0012). A change is judged by the metadata **of
   its reference baseline**.
3. **Editable is a working state inside a change.** A node is modified (`update_node`, `delete_node`,
   `add_link`, `remove_link`) only when its effective state is editable. Persisted versions are never
   editable: a change **reopens** the node with a `transition_node` proposal into an editable state,
   edits it, and must move it to a non-editable state before it is applied. The effective state is
   the stored state plus the change's non-rejected transitions in item order. One function
   (`Graph.walk`) implements the rules; `AddItems` runs it for early feedback and `Apply` for
   authority. Nodes with no state yet (created before the lifecycle) are unmanaged: editable, no
   leftover check.
4. **Attachment.** A change is attached to the nodes it modifies (`change_node`, filled by
   `AddItems`; `GetChangeNodes`, `ListNodeChanges`). **Several open changes may be attached to the
   same node**: conflicts are detected when they are applied (`checkHead`, then rebase / merge,
   ADR 0009). The change stays an unversioned entity; its status now follows a machine
   (`draft → active → applied | abandoned`, only `Apply` applies).
5. **Transitions are checked when the change is applied**, for the actor who applies it: the
   transition exists, the actor holds its permission (`type:action`, default `node:transition`;
   callers without identity are trusted internal services), the node has the required attributes
   and links, the CEL guard holds. A node created with `create_node` starts in the initial state
   (or in `state` when a transition `initial → state` exists).
6. **Documents.** A type with `document.contains` embeds nodes through outgoing `contains` links. The
   document gets a new version when its child set changes (outgoing links belong to the source
   version). A document transition with `children.states` is **validated, not cascaded**: every
   contained child, **in the result baseline** (so children moved by the same change count with
   their new state), must be in one of the states.
7. **Direct writes are closed** for lifecycle types: `CreateNode`, `UpdateNode` and `CreateLink`
   RPCs refuse them (`FailedPrecondition`); in-process seeds and imports keep the Go API.
8. **Blackboard.** Node views carry `state` and `editable` in CEL (`node.state`, `node.editable`,
   proposals `p.node.state`), the DSL gets `ctx.proposeTransition(key, state)` and the LLM prompt
   knows `transition_node`.

## Consequences

- A lifecycle is opt-in per type (`domains/alm.yaml`: `Requirement` family, `Release`). A database
  seeded before it keeps the old `alm@1.0.0`; publish a new domain version to adopt it.
- Actions that update approved nodes must reopen them first (llm actions are told so).
- New default policy `node:transition` (contributor / methodologist / approver of the org); existing
  policy stores need it added from the Access screen, as for ADR 0012 and 0013.
- Not done: refusing to publish a domain that removes a state still used by nodes (the registry has
  no access to the graph), a change-scoped read of the effective state in the UI, cascading
  transitions, suspect-link review workflow on state changes.
