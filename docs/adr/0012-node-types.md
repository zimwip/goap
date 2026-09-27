# ADR 0012 — Node types: qualified references resolved from the published domains

**Status**: accepted, being implemented · **Date**: 2026-09

## Context

A node is an instance of a node type, and a methodology works on the node types of a domain. Node types have been
represented three times: in the definition of a domain (the registry), as `NodeType` nodes projected onto the graph
(the metadata the graph enforced: lifecycle, validators, search, editor, subtyping), and by a bare name in every
reference (`Node.Type = "Requirement"`, CEL literals, `expects.produce.nodeType`, link type ends). The projection
existed so that the graph could "loop on itself": describe its own types with its own nodes. It brought a bootstrap
problem, a synchronisation (`pkg/metamodel`) that could drift from the definitions, two sources for `extends`
(a property and links), and name-only resolution across every namespace.

A reference by name is enough: a node type is identified by the domain that declares it and its name, and the domain
definitions are already graph data (ADR 0023). Nothing needs to be projected.

Question answered (CLAUDE.md, rule 8): **WHAT** is being changed — how a node says what it is.

## Decision

### 1. A node type is referenced as `<namespace>@<NodeType>`
- One domain per namespace (ADR 0013): the prefix names both the namespace and the domain that declares the type
  (`alm@Requirement`, `organisation@OrgUnit`, `platform@MCP`).
- `Node.Type` is **composite**: the namespace and the key of the node type in that namespace (`TypeRef{Namespace,
  Name}`, written `alm@Requirement`). A node lives in the namespace of the domain that types it, so the namespace of
  its type is its own.
- Every reference to a node type is qualified: `extends`, link type ends (`from`, `to`), `document.contains`,
  `expects.produce.nodeType`, the literals of CEL (`n.type == "alm@Requirement"`, `"alm@Requirement" in n.types`),
  `x.types`. A reference may cross domains (`alm@Component` has an `owner` link to `organisation@OrgUnit`).
- Link types are referenced the same way, with the prefix of the domain that declares them (`alm@verifies`,
  `organisation@owner` from a node of any domain to an `organisation@OrgUnit`).
- Inside a domain definition, a bare name is a type of that domain (`extends: Requirement` in `alm`), a qualified one
  a type of another (`extends: base@Item`). `domain.TypeRef` parses and qualifies references; `pkg/typecat` is the
  catalogue.

### 2. The registry is the reference; the services hold an in-memory copy
- The **registry** is the reference of the node types: the published version of each domain (ADR 0013, 0023). It
  resolves each qualified type to its model: properties, `extends` chain, lifecycle (with its algorithm guards and
  actions), property validators, document, change control, search declarations, editor.
- The **type catalogue** is the in-memory copy of that model held by the services that need it: the graph (judging a
  change, the node events of the index), the engine (`x.types`). A service loads it from the registry at startup and
  keeps it in sync with the registry's events (`goap.registry.domain.published`, archived, ...); it does not read the
  domain definitions from the graph itself. The code is a library (`pkg/`) shared by the registry and the services.
  The IDE asks the registry.
- A change is judged by the catalogue **in force when it is checked**: when a version is written, and again when the
  change is applied. Publishing a domain version applies to the changes checked afterwards.
- Nothing is projected: no metadata nodes the graph enforces, no `M:` / `D:` keys, no `pkg/metamodel`
  synchronisation, no `goap.graph.nodetype.changed` event. The only node type nodes are the `domain@NodeType`
  definition nodes of the domain versions (ADR 0023), which the registry keeps.

### 3. The existence rule
- A node written in a change (declared, written, merged, committed, seeded) must have a type the catalogue resolves,
  and its namespace must be the type's. Otherwise the write is refused (`ErrInvalid`).
- A link must have a known link type whose ends accept the types of the two nodes.
- The registry applies the same rule when a methodology or a domain is saved and published: every qualified reference
  must resolve (ADR 0013 §3).

### 4. Meta-domains close the loop without projecting
- The definitions of methodologies and domains are nodes too (ADR 0023), typed by two **meta-domains** built into the
  platform: `methodology` (`MethodologyVersion`, `Agent`, `Action`, `Condition`, `Goal`) and `domain`
  (`DomainVersion`, `NodeType`, `LinkType`, `Lifecycle`, `Algorithm`, `AlgorithmInstance`).
- They ship with the code (embedded YAML) and are always in the catalogue, before anything is loaded from the registry:
  the registry writes its definition nodes into a graph that does not need the registry to check them, so there is no
  bootstrap cycle (the registry stores in the graph, the graph loads its catalogue from the registry).

## Consequences

- One reference for a type: the registry. Changing a type is publishing a domain version; the copies follow the
  registry's events and are reloaded at startup, so a missed event lasts until the next one or the next restart.
- Until the graph has loaded its catalogue, it accepts only the nodes of the meta-domains (the registry's own writes)
  and refuses the others as unknown types; it retries loading while the registry is not up.
- `Node.Type` changes format: existing graphs are not migrated, they are reset and seeded again (as for ADR 0023).
- An unknown type is an error instead of an untyped node without lifecycle or validators.
- Removed: `pkg/metamodel` (projection, `Supertypes`, `TypeNamespace`, `CreateObject` on NodeType nodes,
  `ApplyNodeTypes`), the `NodeType` meta-types and links of `domains/platform.yaml`, the ABAC resource `nodetype`,
  the engine's `SupertypesCache`, the IDE resolvers that scanned the head graph for `NodeType` nodes.
