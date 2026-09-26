# ADR 0023 — The registry in the graph

## Context

Methodologies and domains lived in the registry's SQL tables (ADR 0006); the graph only held a projection of the published ones
(ADR 0011). What describes the enterprise is graph data, changed through changes (CLAUDE.md, rule 4), and the graph's own metadata is
now graph data too (ADR 0022).

## Decision

1. Each version of a methodology or a domain is a node of the `platform` namespace: `MethodologyVersion` (`MV:<name>@<version>`) and
   `DomainVersion` (`DV:<name>@<version>`). The definition is the node's content, `status` (draft, published, archived) a property.
2. `registrysvc.GraphStore` implements the registry's `Store` and `DomainStore` on the graph: every save, status change and deletion is a
   change applied on main (retried once on a conflict), reads come from a snapshot of the head of `main` (`pkg/graphsnap`). The registry
   service keeps its validation, compilation and publication rules and events; it has no database and reaches the graph through a client.
3. A deleted draft keeps its node, marked deleted, because a node key is never freed on a versioned graph; saving the version again revives it.
4. Immutability of published versions stays a rule of the registry service. The edit history of a draft is the version history of its node.
5. The published elements (`M:` / `D:`, ADR 0011) remain derived from the definition and are projected on publication as before.
6. The registry bootstraps from `domains/*.yaml` and `methodologies/*.yaml` once the graph answers (retrying), instead of failing at start.

## Consequences

- Methodologies are versioned, journaled and reviewable like any node; a draft edit costs a change and a baseline on main.
- The registry depends on the graph: without it nothing is read or written. The graph, which projects on the registry's events, retries at start.
- Existing registry databases are not migrated: definitions are imported again from the YAML files, drafts of the old tables are lost.
- Not done: decomposing the definition into the element nodes as the source of truth (the elements are still a projection), and the engine
  and the web reading the graph directly instead of the registry API. Both can follow without changing the store.
