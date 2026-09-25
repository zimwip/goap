# ADR 0016 — Organisation namespace and sub-changes

**Status**: accepted · **Date**: 2026-09 · Builds on ADR 0015 (namespaces, change branches).

## Context
Work on a change crosses organisational boundaries: the nodes it impacts belong to different teams.
The organisation is hierarchical and is not part of the delivery domain: it needs its own
namespace, and nodes of other namespaces must be able to reference it (a component is owned by a unit).

## Decision
1. **Organisation namespace** (`organisation`, domain `domains/organisation.yaml`): `OrgUnit` nodes
   (name, kind: company, direction, department, team). The hierarchy is the `part_of` link, **child to
   parent**: outgoing links belong to the source version (ADR 0003), so reorganising a unit only
   versions that unit. `SeedDemo` loads a small hierarchy.
2. **Ownership** is a cross-namespace link `owner` from any node to an `OrgUnit` (it replaces the
   free-text `owner` properties of the ALM domain).
3. **Sub-change**: a change has `parentId` and `ownerOrg` (key of an `OrgUnit`; it must exist). A
   sub-change belongs to the namespace of its parent; the parent must have a branch of its own; the
   sub-change forks its own branch from the parent branch and, when applied, is merged into it (ADR
   0015 mechanics, including `merge_pending`). When the parent has a unit too, the unit of the
   sub-change must be the same or below it (`part_of` chain).
4. **Parent lifecycle**: a parent cannot be applied while it has open sub-changes (apply or abandon
   them first); abandoning a parent abandons its open sub-changes and their branches. The parent
   starts from the head of its own branch, so what its sub-changes merged is part of its result before
   it is merged into main.
5. **Split**: `SplitByOwner` (`SplitChange` RPC) creates one sub-change per unit owning nodes the
   change has an impact on (`owner` link of the impacted version), with copies of the impacts of its
   nodes (derived from the parent's). It is idempotent per unit; impacts on unowned nodes stay with
   the parent. Running an agent per sub-change uses the existing sub-agent mechanism on the
   sub-change (an engine builtin for the split is not provided yet).

## Consequences
- Migration `0007_subchange` / SQLite `0006_subchange`: `change_set.parent_id`, `owner_org`.
- A dedicated organisation service is not needed: the graph, its namespace rules and a web editor
  are enough. The iam `Organization` stub (M2) is unrelated (tenant isolation).
