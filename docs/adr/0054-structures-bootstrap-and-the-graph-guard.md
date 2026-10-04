# ADR 0054 — Structures, bootstrap and the guard of the graph

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0012/0013 (domains, type catalogue), ADR 0039 (project), ADR 0040 (mandatory parenting), ADR 0049
(mandatory change). Supersedes ADR 0049's `LinkOrphanUnits` and the `organisation@owner` link, and ADR 0039's
"empty project resolves to the root project" and its `Engine.Start` project gate.

## Context

The responsibilities are split across three components: the **registry** defines the domains, the **graph service**
stores the graph and enforces its rules, the **engine** executes. Three rules must hold for every piece of graph data,
and must be the graph's, not its callers':

1. every modification goes through a change: every node version references one;
2. every node version is owned by an organisational unit (who is responsible);
3. every node is created in a project (where the work happens).

Before this ADR rule 1 was enforced by the Go write paths only (ADR 0049): `node_version.change_id` was nullable and
referenced nothing, `link.change_id` too, a branch membership (`node_branch`) named no change. Ownership was an
optional cross-namespace `organisation@owner` link; nothing said in which project a node was created. A change's
`owner_org` / `project_id` were optional text where "empty" meant "the default", resolved at read time, and the
existence checks did not check the type (a `Policy` passed as an owner). The organisation and project types were
hard-coded in three packages. `ORG-DEFAULT` and `PROJ-ROOT` were seeded by `graphsvc.SeedDefaults` in a background
goroutine after other seeds had written (hence `LinkOrphanUnits`), the project rule lived in the engine
(`Engine.Start`), and the registry had no way to say which types build the organisation and the projects.

## Decision

### Structures are tagged by the domains (registry)

A node type may carry a `structure` tag (`methodology.NodeType.Structure`, proto `StructureTag`):
`{kind: organisation | project, parent: <link type of the domain, from and to the type>, root: <key>, selfParent}`.
The schema check refuses an unknown kind, a tag twice in a domain, a missing root or a parent link that does not go
from and to the type; the type catalogue (`pkg/typecat`) refuses a second domain tagging a kind already tagged and a
catalogue without both, so the registry reports it when the domain is saved and never publishes it. The built-in
organisation domain tags `OrgUnit` (`part_of`, root `ORG-DEFAULT`) and `ProjectUnit` (`project_part_of`, root
`PROJ-ROOT`, self-parent). `TypeCatalog.Structure(kind)` and `IsA(typ, base)` give the graph what it needs; an
untyped graph (tests, tools) uses `domain.BuiltinStructures`. A subtype belongs to its structure (`User extends
OrgUnit` is a unit).

### The bootstrap (graph service)

`Graph.Bootstrap` creates both roots in **one transaction**, through one applied change per namespace the roots live
in (`Bootstrap`, administrative, held by the root unit and acting in the root project it creates): the root unit owns
itself and the root project, both were created in the root project, the root project links to itself. It is
idempotent (a graph with both roots is left alone, one with a single root is refused) and safe between processes (a
conflict is read back). The services run it at start, before serving and before any seed (`cmd/graph`,
`cmd/goap-dev`); `CreateChange` and `MergeBranch` run it too, so no change can precede it. Every seed is then an
ordinary change: the demo organisation hangs under `ORG-DEFAULT` from its creation (`LinkOrphanUnits` is gone).

### The guard (graph service, whatever the storage)

`graph.New` wraps every repository transaction in a guard (`pkg/graph/guard.go`), so the rules hold for the in-memory
store, SQLite, PostgreSQL and any storage to come, with or without constraints:

- a node version, a link, a branch membership (`JoinBranch` now takes the change whose merge makes the version join)
  and a node origin must name a change, and the change must exist;
- a node version is owned by a unit and its node was created in a project, nodes of the tagged structures. A version
  that names neither gets them from the version it follows, else from its change (the unit holding it, the project it
  acts in); the project of a node never changes; the owner changes only through a change (`NodeWrite.Owner`,
  `NodeEdit.Owner`, proto `owner`: the key of the unit it is transferred to);
- a change names its unit and its project (never empty) and both are nodes of their structures.

Existence and type are checked when the transaction ends, before it commits, like deferred foreign keys: that is what
lets the bootstrap write the two roots, each referencing the other and itself.

`CreateChange` resolves a change's scope once and stores it (`scopeChange`): an unset owner is the parent change's,
else the root unit; an unset project the parent change's, else the **default project**. The default project is the
`ProjectUnit` flagged `default: true` (`domain.PropDefaultProject`; the smallest key if several, the root project if
none); the bootstrap flags the root project, and an administrator moves the flag in one change (Project tab, "Make
default project"). The engine no longer refuses a process without a project (`ErrNoProject` is gone): its change acts
in the default project, and the run takes the change's project for its role checks.

### The storage repeats it

The graph schema starts from zero (one `0001_schema.sql` per dialect; the earlier migrations are folded into it, no
data is carried over): `node_version.change_id`, `link.change_id`, `node_branch.change_id` are `NOT NULL` foreign keys
to `change`, `node_version.owner_id` and `node.project_id` `NOT NULL` foreign keys to `node`, `change.owner_org` /
`change.project_id` non-empty. The foreign keys a bootstrap must satisfy in one transaction are
`DEFERRABLE INITIALLY DEFERRED`. These constraints are a second line: the guard does not rely on them.

### Ownership replaces the `owner` link

`organisation@owner` is removed from the organisation domain. Ownership is the owner of the version: `SplitByOwner`
groups a change's impacts by the owner of their pre versions (a node owned by the unit holding the change, or by a unit
outside it, stays with the parent), an `organisation@Adapter` belongs to the unit owning it (`mcpsvc.BuildSnapshot`,
`graphsvc.SeedAdapter`, the Organisation tab), and `graph.Node` / proto `Node` carry `owner` and `project` (node ids).

## Consequences

- Rule 1 holds by construction at two levels (the guard, the schema); rules 2 and 3 hold in the guard and the schema
  alike; no caller-level exemption remains (test fixtures write through a change too).
- No service names the organisation or the projects itself: the graph reads the structures from its catalogue, and
  the services reading them (`pkg/access`, `internal/mcpsvc`) ask the graph service (`GetStructures`: both structures
  with the types belonging to each, a `User` being a unit, `domain.Structures`) and build their snapshots from the
  answer: units and projects are the nodes of those types, their hierarchies the parent links named, the roots the
  keys named. The catalogue requires both structures to live in one namespace (the Assignment links a unit and a
  project), so these services read one head. The built-in names stay in `pkg/domain` (`TypeOrgUnit`, ...) for the
  untyped graph and for the seeds writing organisation data.
- An existing database must be recreated (`make devlocal-reset` for the local mode): the new schema file cannot be
  applied over the old one, and fails loudly if tried.
- Every change has a real project, so a role check on a change always has a project chain; "select a project before
  acting" remains a choice of the web (the project selector), no longer a refusal of the engine.
- Baselines follow the same rule: see ADR 0056 (a baseline is the state a change leaves; the empty state before the
  first change is the empty baseline id, which nothing stores).
