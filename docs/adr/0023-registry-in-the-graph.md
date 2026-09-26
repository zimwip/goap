# ADR 0023 — The registry in the graph

## Context

Methodologies and domains lived in the registry's SQL tables (ADR 0006); the graph only held a projection of the published ones
(ADR 0011). What describes the enterprise is graph data, changed through changes (CLAUDE.md, rule 4), and the graph's own metadata is
now graph data too (ADR 0022).

## Decision

1. A version of a methodology or a domain is a **header node**, `MethodologyVersion` (`MV:<name>@<version>`, namespace `methodology`) or
   `DomainVersion` (`DV:<name>@<version>`, namespace `domain`), holding the scalar fields of the definition, its `status` (draft, published,
   archived) and timestamps, and **one node per element** (same namespace as its header): `DefCondition`, `DefAction`, `DefGoal`, `DefAgent` (with its triggers), `DefNodeType`, `DefLinkType`,
   `DefLifecycle`, `DefAlgorithm`, `DefAlgorithmInstance`, keyed `<header key>/<kind>/<name>`, ordered by a `position` property and tied to
   the header by `defines` links. The definition a caller gets is assembled from these nodes (`internal/registrysvc/defs.go`) and round-trips
   exactly, which a test checks on every methodology and domain of the repository. A version stored as one document by an earlier build is still read.
2. `registrysvc.GraphStore` implements the registry's `Store` and `DomainStore` on the graph: every save, status change and deletion is a
   change applied on main (retried once on a conflict), reads come from a snapshot of the head of `main` (`pkg/graphsnap`). The registry
   service keeps its validation, compilation and publication rules and events; it has no database and reaches the graph through a client.
3. A node key is never freed on a versioned graph, so nothing is deleted: a deleted draft keeps its header marked `deleted`, an element that leaves a
   draft is marked `removed`, and saving the version again, or the element again, revives the node instead of creating one.
4. Immutability of published versions stays a rule of the registry service. The edit history of a draft is the version history of its node.
5. **Registry-managed definitions.** Node types, link types, lifecycles and algorithms of a domain are elements of a stored version, managed through
   the registry; a methodology refers to its domain by an attribute (`domainRef`, resolved when it is published). The stored nodes need no metadata of the
   graph itself: no seed, no lifecycle, no chicken-and-egg. The graph still enforces its *data* nodes against the `NodeType` (and `LinkType`, `Lifecycle`)
   nodes a publication projects (ADR 0012, 0014, 0022); those are derived, not authored.
6. The published elements (`M:` / `D:`, ADR 0011) remain derived from the definition and are projected on publication as before.
7. The registry bootstraps from `domains/*.yaml` and `methodologies/*.yaml` once the graph answers (retrying), instead of failing at start.

## Consequences

- Methodologies are versioned, journaled and reviewable like any node, and per element: editing one action gives a new version of that node only,
  saving an unchanged definition changes nothing. A draft edit costs a change and a baseline on main.
- The registry depends on the graph: without it nothing is read or written. The graph, which projects on the registry's events, retries at start.
- Existing registry databases are not migrated: definitions are imported again from the YAML files, drafts of the old tables are lost.
- Two sets of nodes exist for a published methodology: the authored `Def*` elements (per version) and the run-time projection (`M:` / `D:` keys,
  one per methodology, ADR 0011) derived from them on publication.
- Not done: the engine and the web reading the graph directly instead of the registry API; drafts as open changes on their own branch; the graph
  enforcing the immutability of a published version (it is still a rule of the registry service).
