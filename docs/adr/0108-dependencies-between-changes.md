# ADR 0108 — Dependencies between changes: `depends_on`, the merge order, runs that wait

**Status**: accepted, implemented (see "Not done") · **Date**: 2026-10 · Extends ADR 0106 (methodologies integrating
methodologies: its "Not done" named `depends_on`), ADR 0102 / 0103 (the merge of a change and of a sub-change), ADR
0036 §3 (coupling by events), ADR 0064 (condition libraries)

## Context

A release (ADR 0106) integrates its candidates as sub-changes, and they merge into it one by one. Nothing said that a
candidate needs the work of another: the release manager had to know it and merge them in the right order, and a
change outside any release had no way to wait for the one it builds on. The order of the merges was the only
expression of a dependency, and the order in which the work itself is carried out had none.

## Decision

### 1. The relation, stored on the dependent change

`Change.DependsOn` lists the changes whose work must have reached the branch the change merges into before it merges
(column `change.depends_on`, jsonb / text, migration `0003_change_depends_on.sql` in both dialects; proto
`Change.depends_on`). `Graph.SetChangeDependencies(id, deps)` (operation `change.depend`, doer, asked on the change)
replaces the list of an open change (draft or active, ErrConflict otherwise) and journals it in a `change.updated`
entry (field `dependsOn` `{from, to}`). It refuses (ErrInvalid) the change itself, one of its ancestors (it merges
after it anyway), one of its sub-changes (a change already waits for them) and a cycle through the dependencies
already stored; an abandoned change (ErrConflict: it never lands). Dependencies may cross namespaces and projects.
`AttachChange` refuses to put a change under a parent (or one of its ancestors) it depends on or that depends on it.

### 2. Satisfied: the work reached the branch the change merges into

A dependency is **satisfied** when its work has reached the branch the dependent change merges into
(`dependencyOf`, `graph/graph/dependency.go`): the work of an applied change is on its parent's branch, that of an
applied parent on its own parent's, and so on, so the dependency is satisfied when that walk reaches main (every change
of its chain applied) or the dependent's parent (both candidates of one release, the first merged into it). A
dependency merged into a release that has not landed does not satisfy a change outside that release
(`domain.Dependency.Reason` says so).

### 3. The merge waits for its dependencies

`checkMerge` (ADR 0102, `graph/graph/mergecheck.go`) refuses, before asking the guardians, a change with a dependency not
satisfied (ErrConflict naming each one and what it waits for). The rule holds for every merge, a root change into
main as a sub-change into its parent, whoever asks (`IntegrateChange`, `Apply`, `MergeChange`).

### 4. The order of the merges, and of the work

- **Merge order.** `domain.DependencyOrder` sorts changes so that each comes after those of the list it depends on
  (ties by creation, then id). `Graph.ChangeDependencies(id)` (RPC `GetChangeDependencies`) gives the dependencies of
  a change as it sees them, the open changes depending on it, and its open sub-changes in that order, each saying
  whether its own dependencies are satisfied: the order a release merges its candidates in.
- **The guards see them.** The blackboard carries the facet `dependencies` (`domain.FacetDependencies`, filled by the
  graph in `BlackboardIn` and at the merge): CEL `dependencies` (`{id, title, methodology, status, state, parent,
  satisfied, reason}`) in conditions, lifecycle guards and the guardians of the chain (`guard.Facts.Dependencies`), and
  `change.dependsOn` (the ids).
- **The work waits.** The condition library `dependencies` (`imports: [dependencies]`, ADR 0064) gives
  `dependencies_met` and `dependencies_pending`. No action produces `dependencies_met`, so a step needing it **waits**
  (ADR 0036 §3, `awaiting.go`) rather than failing at the merge. `sdlc` (0.5.6) imports it and its `application` step
  needs it.
- **Woken when a dependency moves.** `Engine.relay` (`engine/engine/companion.go`) carries the events of a change to the
  open changes depending on it, as it does to its parent and sub-changes: their waiting runs are tried again, and their
  methodologies may subscribe with `on: [{event, from: dependency}]` (`methodology.FromDependency`,
  `vars.event.relation` `dependency`). The composition checks of the registry leave `from: dependency` alone: any
  change, of any methodology, may be depended on.

### 5. Where a person sets them

`goap-change` tools `depend` (`{change, add, remove}`) and `dependencies` (read: `dependsOn`, `dependents`,
`mergeOrder`); RPCs `SetChangeDependencies` / `GetChangeDependencies`, `graphsvc.Client`, `engine.GraphPort`; the
change tab's *Depends on* section (`ChangeDependencies.svelte`, `web/src/lib/changeDependencies.ts`): the dependencies
with what each waits for, *Add dependency…* / *Remove*, *Needed by*, and the *Merge order of its sub-changes*.

## Consequences

- The order of the work follows from the relation: a change whose methodology waits for `dependencies_met` stops
  there until what it builds on has landed where it merges, and resumes by itself.
- A release merges its candidates in dependency order; the merge of one before what it needs is refused with the
  reason, not left to the release manager's memory.
- A dependency on a change of another release holds the dependent until that release lands; moving one of the two
  changes (detach, attach, ADR 0106) is how a release manager resolves it.

## Not done

- The assistant does not propose dependencies (no `depend` proposal); `goap-change` and the change tab do.
- A change is not created with its dependencies (`NewChange` has none): they are set once it exists.
- `release-based` does not use the merge order to drive its integration step; its release manager reads it on the
  change tab.
