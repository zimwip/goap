# ADR 0027 — The editor of a node type

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0012 (node types in the graph), ADR 0013 (shared
domains), ADR 0023 (registry in the graph).

## Context
Every node opens in the default node editor of the IDE (properties, relations, lifecycle, history). Some nodes have a
richer editor: an agent or an action of a methodology, a methodology or domain version, an algorithm, an organisational
unit, an MCP, an adapter definition, the access policies. Which editor a node opens in was decided by each screen, and
most screens opened the default one, so a `DefAgent` found by a search or in a change opened as a bare node.

Question answered (CLAUDE.md, rule 8): **WHAT** is being changed — how the nodes of a type are presented is a
declaration of the domain, next to its lifecycle, validators and search properties.

## Decision

### 1. The NodeType names its editor
```yaml
nodeTypes:
  - {name: DefAgent, editor: agent, properties: [...]}
  - {name: OrgUnit, editor: unit, properties: [...]}
```
- `editor` is a name (`^[a-z][a-z0-9_-]*$`, checked by `Schema.check`); empty: the default node editor.
- Inherited through `extends`: a subtype opens in the editor of its nearest ancestor that names one.
- It is carried like the other declarations of a node type: `methodology.NodeType.Editor`, `registry.v1.NodeType.editor`,
  and the `editor` property of the `NodeType` node, which follows the published domain (`metamodel.lifecycleKeys`).
- The domain does not know the IDE: the name is a contract. An unknown name falls back to the default editor.

### 2. The IDE resolves it everywhere
- `web/src/lib/shell/registry.ts` holds the **node editors** (`registerNodeEditor({name, title, open})`): `open` maps a
  node (id, key, type, namespace, properties) onto the tab of an editor, and may name a field to reveal; it returns
  nothing when it cannot show the node (an element removed from its definition, an unknown version).
- `web/src/lib/nodeEditors.ts` `openNode` is the single way to open a node: it reads the editor of the node type from
  the `NodeType` nodes of the head of main (cached per head), calls the node editor, and falls back to the default
  node editor. `generic: true` forces the default editor (history, relations).
- The default node editor offers "Open in …" when the type names another editor.
- The node editors of the platform are registered in `web/src/lib/views/nodeEditors.ts`:

| editor | node types | opens |
|---|---|---|
| `methodology` | `MethodologyVersion`, `Methodology` | the methodology version |
| `agent`, `action`, `condition`, `goal` | `DefAgent`, `Agent`, `DefAction`, `Action`, ... | the element's tab, on the methodology draft |
| `domain` | `DomainVersion` | the domain version |
| `definition` | `DefNodeType`, `DefLinkType`, `DefLifecycle` | the methodology or domain version, on the element |
| `algorithm`, `instance` | `DefAlgorithm`, `DefAlgorithmInstance` | the algorithm / instance tab |
| `unit` | `OrgUnit`, `Adapter` | the unit's page (its adapters) |
| `access` | `Policy`, `User` | the access screen |
| `mcp`, `adapter` | `MCP`, `AdapterDef` | the MCP / adapter definition |

A new editor is a registration in the IDE and a declaration in the domain; the graph does not change.

### 3. Every published domain is on the graph
The editor, like the other declarations, is read from the `NodeType` nodes, which were projected only for the domains a
methodology references. `metamodel.SyncAll` now also projects the latest published version of the domains no
methodology references (`metamodel.PublishedDomains`, implemented by the registry service and its client), so that the
`organisation` domain types its units, adapters, users and policies on the graph.

## Consequences
- `platform` 1.1.0 and `organisation` 1.3.0 declare the editors (and `platform` declares the `MCP` and `AdapterDef`
  types); `methodology-improvement` 1.3.0 references `platform@1.1.0`.
- The domain form edits `editor` and keeps `search` (it was dropped on save before).
