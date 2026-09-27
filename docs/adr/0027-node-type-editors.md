# ADR 0027 — The editor of a node type

**Status**: accepted, implemented · **Date**: 2026-09 ·
Extends ADR 0012 (node types), ADR 0013 (domains), ADR 0023 (methodologies in the graph).

## Context
Every node opens in the default node editor of the IDE (properties, relations, lifecycle, history). Some nodes have a
richer editor: an agent or an action of a methodology, a methodology version, an organisational
unit, an MCP, an adapter definition, the access policies. Which editor a node opens in was decided by each screen, and
most screens opened the default one, so an agent found by a search or in a change opened as a bare node.

Question answered (CLAUDE.md, rule 8): **WHAT** is being changed — how the nodes of a type are presented is a
declaration of the domain, next to its lifecycle, validators and search properties.

## Decision

### 1. The NodeType names its editor
```yaml
nodeTypes:
  - {name: OrgUnit, editor: unit, properties: [...]}
```
- `editor` is a name (`^[a-z][a-z0-9_-]*$`, checked by `Schema.check`); empty: the default node editor.
- Inherited through `extends`: a subtype opens in the editor of its nearest ancestor that names one.
- It is part of the type's model (ADR 0012 §2): `methodology.NodeType.Editor`, `registry.v1.NodeType.editor`, and
  resolved in the type catalogue (`typecat.Type.Editor`, `registry.v1.TypeInfo.editor`).
- The domain does not know the IDE: the name is a contract. An unknown name falls back to the default editor.

### 2. The IDE resolves it everywhere
- `web/src/lib/shell/registry.ts` holds the **node editors** (`registerNodeEditor({name, title, open})`): `open` maps a
  node (id, key, type, namespace, properties) onto the tab of an editor, and may name a field to reveal; it returns
  nothing when it cannot show the node (an element removed from its definition, an unknown version).
- `web/src/lib/nodeEditors.ts` `openNode` is the single way to open a node: it reads the editor of the node's type
  from the IDE's copy of the type catalogue (`web/src/lib/stores/types.svelte.ts`, loaded from the registry's
  `ListTypes`, reloaded when the IDE publishes or archives a domain), calls the node editor, and falls back to the
  default node editor. `generic: true` forces the default editor (history, relations).
- The default node editor offers "Open in …" when the type names another editor.
- The node editors of the platform are registered in `web/src/lib/views/nodeEditors.ts`:

| editor | node types | opens |
|---|---|---|
| `methodology` | `methodology@MethodologyVersion` | the methodology version |
| `agent`, `action`, `condition`, `goal` | `methodology@Agent`, `@Action`, `@Condition`, `@Goal` | the element's tab, on the methodology draft |
| `unit` | `organisation@OrgUnit`, `@Adapter` | the unit's page (its adapters) |
| `access` | `organisation@Policy`, `@User` | the access screen |
| `mcp`, `adapter` | `platform@MCP`, `@AdapterDef` | the MCP / adapter definition |

The editors of the built-in types are declared in their built-in definitions (ADR 0012 §4). Domains are not graph
data (ADR 0023): they open from the Domains explorer, not through a node. A new editor is a
registration in the IDE and a declaration in a domain.

## Consequences
- The domain form edits `editor`, and keeps `search` on save.
- The IDE resolves lifecycles, properties and editors from the same catalogue, by qualified type.
