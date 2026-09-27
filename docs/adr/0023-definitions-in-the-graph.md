# ADR 0023 — Methodologies are graph data; domains are the registry's

**Status**: accepted, implemented · **Date**: 2026-09

## Context

Methodologies and domains must be editable from the IDE, versioned and reviewable. They are not the same kind of
thing:

- a **methodology** describes how the enterprise works (CLAUDE.md, rule 4): it is changed through changes, journaled,
  and improved by the self-observation agent (ADR 0011), element by element;
- a **domain** is the definition of a graph: the namespace, its node types, link types, lifecycles and algorithms. Each
  graph holds it in memory (the type catalogue, ADR 0012 §2) to keep its data coherent and apply its rules. It is the
  model the data is checked against, not data itself: storing it as nodes needs a meta-domain to type them, a type
  catalogue to accept them, and brings nothing the registry does not already give (versions, drafts, publication).

## Decision

1. **A methodology is nodes of the meta-domain `methodology`.** A version is a node `methodology@MethodologyVersion`
   (key `MV:<name>@<version>`, namespace `methodology`) holding the scalar fields, the `status` and the timestamps,
   and one node per element: `Agent` (with its triggers), `Action`, `Condition`, `Goal`. Elements are keyed
   `<version key>/<kind>/<name>`, ordered by a `position` property and tied to their version by `methodology@defines`
   links. The meta-domain is built in (ADR 0012 §4); there is no second representation of a published version.
2. **A domain is kept in the registry's database.** A version is a row of `domain_version` (name, version, status,
   the definition as JSON, timestamps), in PostgreSQL (`internal/registrysvc/migrations`, `GOAP_DB_DSN`) or SQLite in
   the local mode (`migrations_sqlite`, ADR 0010); without a database the registry keeps them in memory
   (`registrysvc.SQLDomainStore`, `MemoryStore`). There is no `domain` namespace, no `DV:` node and no `domain`
   meta-domain. The graph gets the domains from the registry (the type catalogue and its events), never from its own
   content.
3. **Lifecycle of a version** (both): `draft` → `published` (validated, immutable) → `archived`. An invalid draft can
   be saved, not published. The engine executes the latest published version of a methodology; a process keeps the
   version it started with (`methodology@version` in its journal). The latest published version of a domain is in
   force.
4. **The registry keeps the rules.** `registrysvc.GraphStore` implements the methodology store on the graph: every
   save, status change and deletion is a change applied on `main` (rebuilt and retried up to three times when `main` moved), reads come from a
   snapshot of the head of `main` (`pkg/graphsnap`). The registry validates (a list of anomalies with field paths,
   used by the editor), compiles, publishes and emits the `goap.registry.*` events.
5. **Nothing of a methodology is deleted**: a node key is never freed on a versioned graph. A deleted draft keeps its
   version node marked `deleted`; an element that leaves a draft is marked `removed`; saving it again revives the
   node. A deleted domain draft is a deleted row.
6. **YAML is the exchange format** (`Import*` / `Export*`); the registry imports `domains/*.yaml` then
   `methodologies/*.yaml` at startup (missing versions only), once the graph answers.
7. **Access**: ABAC resources `methodology` and `domain` (actions `write`, `publish`, `delete`).

## Consequences

- Methodologies are versioned per element: editing one action gives a new version of that node only; the edit history
  of a draft is the version history of its nodes. They are what the self-observation agent improves (ADR 0011 §2).
- Domains are versioned as a whole, by the registry; their history is their versions.
- The registry builds the type model from its domains and serves it (ADR 0012 §2); what runs is the published
  definition, there is nothing to keep in sync.
- The registry depends on the graph for the methodologies and on its database for the domains.
- Changing the representation (from `Def*` nodes, the `M:` / `D:` projection, then the `DV:` domain nodes) is not
  migrated: graphs and registry databases are reset and seeded again.
- The other services read definitions and types through the registry (API and events), never the stored nodes or rows
  directly.
- Not done: drafts as open changes on their own branch; the graph enforcing the immutability of a published version
  (a rule of the registry).
