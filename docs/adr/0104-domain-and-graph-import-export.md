# ADR 0104 — Import and export of domains and of the graph

**Status**: accepted, implemented · **Date**: 2026-10

## Context

1. A domain could be exported as YAML (`ExportDomain`) and imported (`ImportDomain`) through the registry API, but the
   web offered no import, and JSON, the format the other exports of the platform use (PROV-O, the audit trail), was not
   offered.
2. The graph had no export or import at all: moving the content of a namespace from one installation to another, or
   restoring a known state, meant replaying changes by hand.

## Decision

### 1. Domains: YAML or JSON

- `ExportDomain` takes a `format` (`yaml`, the default, or `json`); the JSON is the YAML document converted (same names,
  same omissions), so `ImportDomain` reads it back: the parser of a domain (`def.ParseDomain`, a YAML decoder) reads JSON
  too. The response keeps its `yaml` field for the content, whatever the format, and gives its `media_type`.
- Web: the domain editor has *Export* (YAML) and *Export JSON*; the Import tab (`ImportTab.svelte`) imports a
  methodology, a domain or a graph snapshot (`what` param, `importKinds.ts`), reached from the explorers' upload button
  and the commands `import`, `importDomain`, `importGraph`.

### 2. The graph: a snapshot without history, imported as one change

- A **snapshot** (`graph.Snapshot`, format `goap.graph.snapshot/v1`, JSON) is the state of one namespace at one baseline
  (default: the head of main): its nodes named by key (type, lifecycle state, owner unit key, properties) and their
  outgoing links to the other nodes of the snapshot (type, target key, properties). No version, branch, change, review,
  project or history: what a baseline holds, nothing of how it got there. Links to another namespace are left out.
  `Graph.ExportSnapshot`, RPC `ExportGraph` (namespace, branch or baseline).
- An import (`Graph.ImportSnapshot`, RPC `ImportGraph`) **creates one change** on main, held by the caller's unit in
  their active project (or the ones the request names), and works through the ordinary operations of a change (ADR
  0076, 0079): a node the namespace lacks is created (`ImpactNodeCreate`), a node it holds is checked out and brought to
  the snapshot (properties patched, removed ones cleared, owner transferred, outgoing links to nodes of the namespace
  added, updated or removed, the lifecycle state reached by one transition), a node equal to the snapshot is left alone,
  and the nodes the snapshot does not name are untouched (a node is never deleted). Nothing is accepted: the change stays
  active, its impacts proposed, and is reviewed, committed and merged like any other (ADR 0102); landing checks the
  validators and required links as usual.
- What the import cannot carry is a warning, not a failure: an owner unit this graph does not hold (the node goes to
  the unit of the change), a link to a node found neither in the snapshot nor in the namespace, a state one transition
  does not reach. A graph that already holds the snapshot gets no change (`change` unset, every node `unchanged`). A
  failed import leaves no change behind (purged).
- Authorization: every node type the import writes is checked like a creation in the change (`object:create` on its
  project, the gate of the `adminOnly` types, `graphsvc.Handler.checkCreate`), once the change exists; the operations
  themselves pass the operation catalogue of ADR 0103 like any caller's.
- Web: the Baseline explorer exports the head of the selected branch (toolbar, and *Export head* on a branch's context
  menu) and opens the graph import; the result links to the change.

## Consequences

- A namespace can be copied between installations or restored, with a review of what the import changes.
- The import is a merge onto main, never a replacement: removing what the snapshot lacks stays a decision of the people
  reviewing the change (retire through the lifecycle).
- Not done: the import of several namespaces at once, cross-namespace links, an import onto a branch other than main.
