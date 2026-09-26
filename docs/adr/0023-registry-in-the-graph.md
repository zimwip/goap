# ADR 0023 — The registry in the graph

## Context

Methodologies and domains lived in the registry's SQL tables (ADR 0006); the graph only held a projection of the published ones
(ADR 0011). What describes the enterprise is graph data, changed through changes (CLAUDE.md, rule 4), and the graph's own metadata is
now graph data too (ADR 0022).

## Decision

1. A version of a methodology or a domain is a **header node**, `MethodologyVersion` (`MV:<name>@<version>`, namespace `methodology`) or
   `DomainVersion` (`DV:<name>@<version>`, namespace `domain`), holding the scalar fields of the definition and its timestamps,
   and **one node per element** (same namespace as its header): `DefCondition`, `DefAction`, `DefGoal`, `DefAgent` (with its triggers), `DefNodeType`, `DefLinkType`,
   `DefLifecycle`, `DefAlgorithm`, `DefAlgorithmInstance`, keyed `<header key>/<kind>/<name>`, ordered by a `position` property and tied to
   the header by `defines` links. The definition a caller gets is assembled from these nodes (`internal/registrysvc/defs.go`) and round-trips
   exactly, which a test checks on every methodology and domain of the repository. A version stored as one document by an earlier build is still read.
2. **The status is the node's state**, not a property: the `version` lifecycle of the platform domain (`draft`, `editing`, `published`, `archived`,
   `deleted`) is named by every one of these node types (ADR 0014). A persisted draft is not editable: a save reopens the nodes that change
   (`open`: draft→editing), edits them and closes them (`close`), publishing moves the header (from `editing`, with its timestamps) and every live element
   to `published`, archiving moves them to `archived` (final). Because `published` has no way back to `editing`, the graph itself refuses to modify a
   published version, its header and its elements, whoever asks; the registry service's check is the friendly first line.
3. `registrysvc.GraphStore` implements the registry's `Store` and `DomainStore` on the graph: every save, status change and deletion is a
   change applied on main (retried once on a conflict), reads come from a snapshot of the head of `main` (`pkg/graphsnap`). The registry
   service keeps its validation, compilation and publication rules and events; it has no database and reaches the graph through a client.
4. A node key is never freed on a versioned graph, so nothing is deleted: a deleted draft keeps its header marked `deleted`, an element that leaves a
   draft is marked `removed`, and saving the version again, or the element again, revives the node instead of creating one.
5. The edit history of a draft is the version history of its nodes.
6. The published elements (`M:` / `D:`, ADR 0011) remain derived from the definition and are projected on publication as before.
7. **The cycle** (a methodology is a node that references node types, which are nodes) is a bootstrap, not a design problem. A methodology refers to
   types by name and they are resolved when it is published, as an adapter definition refers to its MCP. The node types the registry needs (and the graph's own
   metadata, ADR 0022) are declared by the platform domain, which `metamodel.SeedMeta` projects from the copy embedded in the binary when the graph starts: the
   first change is judged by a baseline with no metadata and accepted, everything after is authored through changes. The registry refuses to write (and its
   bootstrap retries) until that seed is there (`ErrMetadataMissing`).
8. The registry bootstraps from `domains/*.yaml` and `methodologies/*.yaml` once the graph answers (retrying), instead of failing at start.

## Consequences

- Methodologies are versioned, journaled and reviewable like any node, and per element: editing one action gives a new version of that node only,
  saving an unchanged definition changes nothing. A draft edit costs a change and a baseline on main.
- The registry depends on the graph: without it nothing is read or written. The graph, which projects on the registry's events, retries at start.
- Existing registry databases are not migrated: definitions are imported again from the YAML files, drafts of the old tables are lost.
- Two sets of nodes exist for a published methodology: the authored `Def*` elements (per version) and the run-time projection (`M:` / `D:` keys,
  one per methodology, ADR 0011) derived from them on publication.
- The registry API is unchanged and is the stable interface to methodologies and domains; the engine and the web keep using it.
- Not done: drafts as open changes on their own branch. Versions written by the previous build (namespace `platform`) are not read: import them again.
