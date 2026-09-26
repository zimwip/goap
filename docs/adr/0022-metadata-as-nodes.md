# ADR 0022 — The graph's metadata as nodes

## Context

The graph content is changes and nodes with their versions. Its metadata (what may be in it) was only partly in it: node
types were graph-native (ADR 0012), lifecycles were embedded in them, link types existed only in the registry (compile-time lint),
and namespaces were a free string. Moving methodologies and domains into the graph (next step) needs the metadata to be
graph data first, and the graph to describe itself.

## Decision

1. Graph content = `Change` + `Node`/`NodeVersion`. Graph metadata = `Namespace`, `Lifecycle`, `NodeType`, `LinkType`, each a node
   type of the `platform` domain. `NodeType` is a `NodeType`, so the metadata describes itself; the first `NodeType` nodes are created
   as ordinary nodes (a change is judged by the metadata of its baseline, which has none yet).
2. `metamodel.ProjectDomain` (and the own domain of a methodology) also project `LinkType` and `Lifecycle` nodes, with edges
   `NodeType --lifecycle--> Lifecycle`, `LinkType --linkFrom/linkTo--> NodeType`. They are mirrored from the registry like the rest
   until the registry itself moves into the graph.
3. The resolved lifecycle stays embedded in the `NodeType` node: evaluating it needs one read of the baseline. The `lifecycle` edge
   and the `Lifecycle` node are the authored reference; the embedding is their cache.
4. `pkg/graph` enforces link types: an `add_link` of a declared type must join node types the `LinkType` nodes of the change's reference
   baseline allow (subtypes count). A link type nobody declares, and an endpoint whose node type is not on the graph, are not judged.
5. `pkg/graph` refuses a change on an undeclared namespace once the baseline holds `Namespace` nodes; a graph without any keeps accepting
   every namespace. `graphsvc.SeedNamespaces` declares the platform's, the default one and those the graph already holds nodes of.

## Consequences

- A graph created before this has no `LinkType`/`Lifecycle`/`Namespace` nodes until the next publication and seed: nothing changes for it.
- Data violating a link type declared later is not rewritten; only new links are checked.
- Algorithms and their instances stay embedded resolved in `NodeType`/lifecycle nodes; they become nodes with the registry (the next ADR).
