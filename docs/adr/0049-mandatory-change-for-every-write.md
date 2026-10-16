# ADR 0049 — Mandatory Change for every node, version and link write

**Status**: accepted, implemented; the rule stands, its mechanisms are superseded: the guard enforces it below the Go
write paths (ADR 0054, which also removed `LinkOrphanUnits`), the empty initial baseline by ADR 0056, the implicit
changes of `CreateNode` / `UpdateNode` and the standalone `Link` by ADR 0076 / 0077 / 0079 (every node and link write
is an operation of a change the caller names) · **Date**: 2026-10 ·
Builds on ADR 0024 (change impacts), ADR 0029/0030 (impact log), ADR 0040 (user bootstrap, required parent).

## Context

"Every modification is a Change" (this project's second design rule) was enforced only at the RPC gateway
(`internal/graphsvc.refuseDirectWrite`), and only for lifecycle-controlled or access-listed types.
`graph/graph` core — `CreateNode`, `UpdateNode`, `Link` — stayed fully open to direct writes bypassing Change
entirely, used by seeding (`SeedDemo`, `LinkOrphanUnits`) and dominant in the test suite (~60 call sites). This
closes that gap in the Graph Service itself, not just the gateway: no node, node version or link is written
outside the frame of a change, hardwired, with no caller-level exemption.

## Decision

No node, node version or link is written by `graph/graph` outside the frame of a change, hardwired, with no
caller-level exemption. As first implemented:

- **`CreateNode` / `UpdateNode` became `Commit` sugar**, signatures unchanged: each opened, populated and applied a
  single-edit change internally (`Graph.commitEdits`, the core `Commit` itself called), a namespace touched for the
  first time getting an empty baseline. A node so created got a real `ChangeID`, went through the impact log (ADR
  0029/0030) and any `NodeValidator` (ADR 0048), and a lifecycle-typed node a real initial state instead of the empty
  string the old raw write left it at (a fix: `SeedDemo`'s `alm@Requirement` nodes had bypassed the editable-state
  gate).
- **`Commit`'s parenting checks stayed `Commit`-specific**: `CreateNode` / `UpdateNode` called
  `commitEdits(ctx, in, parenting=false)`, so ADR 0040's required parent link (`checkRequiredParent`,
  `checkParentInvariant`) kept its pre-existing scope; `SeedDemo` created `ORG-ACME` with no parent and
  `LinkOrphanUnits` parented it afterwards.
- **`Link` gained a required `domain.ChangeID`**, the one signature break, since a link versions neither endpoint and
  an implicit change would defeat asking which change it belongs to (the `CreateLink` RPC gained a `change_id`). Nodes
  a `Commit` creates link each other freely, cycles included (`NodeEdit.Links` with `ToKey`), so a whole import is one
  change per namespace.
- **Test fixtures** got two helpers in `graph/graph/repos_test.go`: `seedNode`, writing a node in an arbitrary lifecycle
  state for a fixture, and `testChange`, opening a change on a namespace's current head for a test that needs a
  `ChangeID`.

## Since then

- ADR 0054 made the rule a property of the storage: the guard (`graph/graph/guard.go`) checks at the end of every
  transaction that every node version, link, branch membership and node origin names an existing change, and the
  schema repeats it with `NOT NULL` foreign keys; even `seedNode` writes inside a change. Units are created with their
  parent, `LinkOrphanUnits` is gone, and the demo data lives in `platform/devseed`.
- ADR 0056 removed the empty initial baseline (the state before a namespace's first change is the empty baseline id,
  stored nowhere) and `CreateBaseline` (a baseline is the state a change leaves).
- ADR 0076, 0077 and 0079 removed the implicit changes: `CreateNode`, `UpdateNode`, the standalone `Link` and
  `CreateLink` are gone; every node write is an operation of a change the caller names (`ImpactNodeCreate`,
  `ImpactNodeCheckout`, `ImpactNodeUpdate`, `ImpactLinkCreate` / `Update` / `Delete`, ...) editing a draft, the
  versions being written when the change commits. `Graph.Commit` remains the producer path (seeds, `CommitEdits`),
  running its edits as one change through those same operations, and keeps `checkRequiredParent`.
- ADR 0078 removed the `editable` flag, so no reopen-before-edit sequence is needed any more.

## Consequences

- The distinction between "change-shaped" (always) and "carries `Commit`'s producer-level parenting guarantees" is
  explicit in the code, not implicit in which entry point happened to be called.
- Test fixtures write through a change too (`seedNode`, `testChange`, `graphtest`), so the graph tests can replay and
  check every change.
