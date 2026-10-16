# ADR 0113 — Versions in use: a change follows a methodology version, a methodology pins domain versions

**Status**: accepted, implemented (see "Not done"); the check of the published methodologies when a domain is published is replaced by their suspension (ADR 0125 §3) · **Date**: 2026-10 · Extends ADR 0012 / 0013 (domains, versioned),
ADR 0023 (methodologies in the graph), ADR 0072 (the compile cache), ADR 0106 (the guardian chain saved on a change)

## Context

Methodologies and domains are versioned (draft → published → archived), but only the latest published version was
ever used. A change named its methodology by name: every reader (the lifecycle, the goal, the guards, the gates, the
operation roles, the engine) took the latest version, so publishing a new version changed the rules of every open
change mid-way. A methodology was validated and compiled against the latest domains, so a new domain version had to
stay compatible with every published methodology, and a shipped domain that broke an old methodology version stopped
the start: the database had to be reset to take the new embedded versions.

## Decision

### 1. A change follows a version of its methodology

`Change.MethodologyVersion` (column `change.methodology_version` in both dialects, migration
`0004_change_methodology_version.sql`; proto `Change.methodology_version`) is set by `CreateChange`: the version named
(`NewChange.MethodologyVersion`, `CreateChangeRequest.methodology_version`), else the parent's for a sub-change of the
same methodology, else the latest published one, resolved by `ChangeLifecycles.MethodologyVersion` (the registry: a
named version must be published or archived, a draft is refused; "" for a methodology the registry does not know, and
without a registry, as in `platform/cmd/graph`). The change keeps it whatever is published later.

Every reader of the methodology of a change takes a **reference** `<name>@<version>` (`domain.MethodologyRef`,
`SplitMethodologyRef`, `Change.MethodologyRef()`; a bare name is the latest published version): the methods of
`graph.ChangeLifecycles` (lifecycle, default goal, guard, gate, merge, guardians, integrates), the operation roles
(`graphsvc.OperationAuthorizer`), the step dependencies of the history fold (`journal.FoldWith`), the guardian chain
(own entry at the change's version) and `engine.MethodologyPort.Methodology` (registry, its client, and
`StaticMethodologies`, which serves the one version it holds). A methodology name never contains `@`.

### 2. A run keeps the version it started with

`Process.MethodologyVersion` (already journaled) is fixed when the process starts: the change's version when it runs
the change's own methodology, else the latest published one then. `Engine.methodologyOf(p)` reads that version
everywhere the engine read the latest (run, cycle, steps, methods, progress); `Run` no longer refreshes it. The
companions, reactions and relays of a change read the change's version.

### 3. Upgrading a change

`Graph.UpgradeChange(id, version)` (operation `change.upgrade`, doer; RPC `UpgradeChange`, `engine.GraphPort`,
`goap-change/upgrade`, web `graph.upgradeChange`) moves an open change to another published version, the latest when
none is named. The change keeps its state: the target version must follow the same lifecycle and know the state
(ErrConflict otherwise). The upgrade updates the change's own entry in its guardian chain and is journaled in a
`change.updated` entry (`methodologyVersion {from, to}`); nothing happens when the version is the one followed. Its
sub-changes keep theirs (each is upgraded on its own), and a run already started finishes on its version.

### 4. A methodology version pins its domains

`Methodology.Domains` (`domains:` in YAML, `name → version`; proto `Methodology.domains`; attribute `domains` of
`methodology@MethodologyVersion`, built-in domain `methodology` 1.9.0) pins the version of each domain the methodology is
written against. A draft pins what it declares (a published or archived version of a stored domain; a built-in domain
is never pinned: it changes with the platform) and follows the latest of the others. **Publishing pins every
published domain in force** it does not pin yet (`Publish`, the published import), so a published version keeps the
types it was validated against. The registry validates and compiles a version against its pins
(`Service.domainsFor` / `typesFor`; the compile cache key holds them); a version published before the pins existed
follows the domain versions in force when it was published (`domainsInForceAt`). `CreateVersion` drops the pins: a
new version of a methodology moves to the latest domains and is validated there. Consequently a new domain version is
refused only when it would break a published methodology that follows the latest one, and the lifecycle a change
follows is the one of the domain versions of its methodology version.

The graph keeps one type catalogue, the latest published domains: its data follows the domain in force (one graph,
one schema). A pinned domain version is the contract of the definition of the methodology, not a second schema of
the data.

### 5. New shipped versions load at start

Each start of `goap-dev` (and the bootstrap of `platform/cmd/registry`) publishes the domain and methodology files whose
version is not stored yet (`SeedDomains`, then `Seed`), and logs them. With §1-4 nothing stops it any more: the open
changes stay on their versions, the new changes take the new ones. A reset of the database is needed only when its
schema changes.

## Consequences

- A change runs with the rules it started with; the new rules reach it only through an explicit upgrade.
- A methodology version is reproducible: same definition, same domain versions, same compilation.
- The latest published version is the one whose publication is the most recent, not the highest number.

## Not done

- The web shows no version on the change tab and offers no *Upgrade* button yet (the client call exists).
- The methodology editor shows the pins only through the raw attribute; no "move to the latest domains" action on a
  draft.
- An upgrade does not map the state of a change onto another lifecycle.
