# ADR 0023 — Methodologies and domains are graph data

**Status**: accepted, being implemented · **Date**: 2026-09

## Context

Methodologies and domains must be editable from the IDE, versioned, reviewable and journaled like everything that
describes the enterprise (CLAUDE.md, rule 4). The registry validates, compiles and publishes them; where it keeps them
must not need a mechanism of its own.

## Decision

1. **A definition is nodes of its meta-domain.** A version of a methodology is a node `methodology@MethodologyVersion`
   (key `MV:<name>@<version>`, namespace `methodology`) holding the scalar fields, the `status` and the timestamps, and
   one node per element, typed by the meta-domain `methodology`: `Agent` (with its triggers), `Action`, `Condition`,
   `Goal`. A version of a domain is a node `domain@DomainVersion` (`DV:<name>@<version>`, namespace `domain`) and
   one node per element: `NodeType`, `LinkType`, `Lifecycle`, `Algorithm`, `AlgorithmInstance`. Elements are keyed
   `<version key>/<kind>/<name>`, ordered by a `position` property and tied to their version by `defines` links. The
   meta-domains are built in (ADR 0012 §4); there are no `Def*` types and no second representation of a published
   version.
2. **Lifecycle of a version**: `draft` → `published` (validated, immutable) → `archived`. An invalid draft can be saved,
   not published. The engine executes the latest published version of a methodology; a process keeps the version it
   started with (`methodology@version` in its journal).
3. **The registry keeps the rules, the graph keeps the data.** `registrysvc.GraphStore` implements the registry's stores
   on the graph: every save, status change and deletion is a change applied on `main` (retried once on a conflict),
   reads come from a snapshot of the head of `main` (`pkg/graphsnap`). The registry service has no database. It
   validates (a list of anomalies with field paths, used by the editor), compiles, publishes and emits the
   `goap.registry.*` events.
4. **Nothing is deleted**: a node key is never freed on a versioned graph. A deleted draft keeps its version node
   marked `deleted`; an element that leaves a draft is marked `removed`; saving it again revives the node.
5. **YAML is the exchange format** (`Import*` / `Export*`); the registry imports `domains/*.yaml` then
   `methodologies/*.yaml` at startup (missing versions only), once the graph answers.
6. **Access**: ABAC resources `methodology` and `domain` (actions `write`, `publish`, `delete`).

## Consequences

- Methodologies and domains are versioned per element: editing one action gives a new version of that node only; the
  edit history of a draft is the version history of its nodes.
- The definition nodes are what the type catalogue reads (ADR 0012 §2) and what the self-observation agent improves
  (ADR 0011 §3). What runs is the published definition; there is no projection to keep in sync.
- The registry depends on the graph: without it nothing is read or written.
- Changing the representation (from `Def*` nodes and the `M:` / `D:` projection) is not migrated: graphs are reset and
  seeded again.
- Not done: the engine and the web reading the definitions from the graph instead of the registry API; drafts as open
  changes on their own branch; the graph enforcing the immutability of a published version (a rule of the registry).
