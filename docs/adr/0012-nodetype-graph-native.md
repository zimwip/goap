# ADR 0012 — NodeType as the metadata layer of the graph

**Status**: accepted · **Date**: 2026-09

## Context

ADR 0011 made the whole meta-model (`Methodology`, `Agent`, `Action`, `Goal`, `Condition`,
`Trigger`, `NodeType`) a **projection**: the registry is the source of truth for every
element, and `pkg/metamodel.Sync` mirrors it onto the graph, reconciling (create, update,
delete) on every publication. That is the right model for elements agents *execute*
(`Action`, `Agent`, `Condition`, `Goal`, `Trigger`): their definition changes only through a
published methodology version.

`NodeType` plays a different role: it is the **domain model** (`Requirement`,
`Component`, `Release`…) that data-layer nodes are instances of, and that subtyping
(`extends`) organizes. Modeling it purely as a registry projection means the domain's
ontology can only evolve by republishing a methodology, and a data node's type is only a
free-form string (`domain.Node.Type`), with no traceable link to the type definition it
instantiates. To make graph modeling more expressive, `NodeType` becomes **graph-native**:
its nodes live in the graph as ordinary versioned elements — a **metadata layer** —
authored directly, and data-layer nodes reference their type through a real edge.

Two sibling projects (`ailm`, `aifact`) were reviewed for a precedent. Neither has this
pattern: both keep type definitions in a separate table or service, deliberately decoupled
from instance data (to rename a type without rewriting every instance, and to keep a
type-definition service independently deployable). This design has no existing pattern to
port; it is specific to goap's versioned-graph model, where a type definition is just
another kind of node.

## Decision

**Scope: `NodeType` only.** `Action`, `Agent`, `Condition`, `Goal`, `Trigger` keep the
ADR 0011 registry-projection model unchanged.

1. **The metadata-layer boundary is a node's `Type`, nothing new.** A node is on the
   metadata layer iff its `Type` is one of the 7 meta-model kinds (`pkg/metamodel`), in
   particular `NodeType`. No new `layer` property, no new branch: a `domain.Branch`
   (ADR 0009) encodes a parallel hypothesis of the change axis (an option, a merge), not a
   permanent schema partition.

2. **New link kind**: `LinkInstanceOf` (`"instanceOf"`), from a data node to the
   `NodeType` node it instantiates. `LinkExtends` (`NodeType → NodeType`, subtyping)
   already existed as a projection artifact; it becomes load-bearing.

3. **`Sync`'s reconciliation splits in two.** `Methodology`/`Agent`/`Action`/`Goal`/
   `Condition`/`Trigger` (and their links) stay full create/update/delete, as before.
   `NodeType` (and its `extends` edges) become **create-if-absent only**: once a `NodeType`
   node exists in the graph, `Sync` never updates or deletes it again, even when the
   registry's declared type changes or is removed. The registry's `Domain.NodeTypes`
   becomes the **bootstrap seed** and a **compile-time validation schema**
   (`Methodology.Compile`, cycle/reference checks) — not the runtime authority.

4. **`x.types` / ancestor resolution reads the graph, with a permanent fallback.**
   `pkg/condition`'s `x.types` only reads `domain.Blackboard.Supertypes` (a plain map),
   already decoupled from its source. `metamodel.Supertypes` walks `NodeType` nodes and
   `extends` edges on the main branch to build it; `pkg/engine`'s `SupertypesCache` calls
   it, keyed by the main branch's head baseline, and falls back to
   `Methodology.Supertypes()` (the declared schema) when the graph has no `NodeType`
   nodes yet for that methodology. This fallback is permanent, not a migration shim: it is
   what keeps unsynced graphs (unit tests, freshly seeded demos) working.

5. **Authoring**: `metamodel.ApplyNodeTypes` creates `NodeType` nodes and `extends` edges
   directly on the graph, through the same change/proposal mechanism `Sync` already uses
   (no new graph primitive). `metamodel.LinkToType` adds an `instanceOf` edge from an
   existing data node to a `NodeType`, used by the seed/import path. The engine's item
   resolver (`pkg/engine/items.go`) does this automatically for LLM, human and script
   output: a `create_node` proposal whose type matches a known `NodeType` gets a companion
   `instanceOf` edge for free.

6. **Authorization**: `internal/graphsvc` had no authorization at all. It gains an
   `Authz` field and a new ABAC resource `nodetype` (role `methodologist`, mirroring the
   `methodology` resource), gating only proposals that create/update/delete a `NodeType`
   node or add an `extends` edge. Ordinary domain-node proposals and `instanceOf` edges
   stay ungated.

## Consequences

- A `NodeType` node's version history now reflects direct edits, not republications —
  its own audit trail, separate from the methodology's.
- The registry's declared node types never disappear on a graph write: `Sync` only adds,
  so an environment can run for a long time with the registry's schema and the graph's
  metadata layer diverging (registry entries removed or changed have no effect once
  seeded). This is accepted: the metadata layer, once seeded, is authored on the graph.
- `SupertypesCache`'s fallback is permanent: any methodology never synced (or reset)
  keeps working off the declared schema, so this is not a one-way migration that could
  strand a caller.
