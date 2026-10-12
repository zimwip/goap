# ADR 0016 — Organisation namespace and sub-changes

**Status**: accepted, implemented · **Date**: 2026-09 · Builds on ADR 0015 (namespaces, change branches). §2 (the
`organisation@owner` link) superseded by ADR 0054 (ownership is the owner of the version). §3 and §4 were superseded by
ADR 0081 (a sub-change lands in its parent change's log), itself superseded by ADR 0103, which brings back a branch of
the sub-change forked from its parent's. §5 amended by ADR 0103 (the candidate moves to the sub-change).

## Context
Work on a change crosses organisational boundaries: the nodes it impacts belong to different teams.
The organisation is hierarchical and is not part of the delivery domain: it needs its own
namespace, and nodes of other namespaces must be able to reference it (a component is owned by a unit).

## Decision
1. **Organisation namespace** (`organisation`, a built-in domain, `domains/builtin/organisation.yaml`, ADR 0012 §4): `organisation@OrgUnit` nodes
   (name, kind: company, direction, department, team). The hierarchy is the `organisation@part_of` link, **child to
   parent**: outgoing links belong to the source version (ADR 0003), so reorganising a unit only
   versions that unit. The demo data (`internal/devseed`) loads a small hierarchy.
2. **Ownership** was a cross-namespace link `organisation@owner` from any node to an `organisation@OrgUnit` (it
   replaced the free-text `owner` properties of the ALM domain). Since ADR 0054 there is no such link: every version
   has an owner unit (`Node.Owner`), stamped from the previous version, else the change's unit.
3. **Sub-change**: a change has `parentId` and `ownerOrg` (key of an `OrgUnit`; it must exist). A
   sub-change belongs to the namespace of its parent; the sub-change forks its own branch from the parent
   branch and, when applied, is merged into it (ADR 0015 mechanics). When the parent has a unit too, the unit
   of the sub-change must be the same or below it (`part_of` chain). Since ADR 0103 (after ADR 0081 had made a
   sub-change land in its parent's log), the parent gets a branch when it has none, the sub-change commits on its
   own branch and merges into its parent's like a root change into its target (rebased onto the parent's head,
   then a fast-forward); a conflict leaves it `committed` (the former `merge_pending`).
4. **Parent lifecycle**: a parent cannot be merged while it has open sub-changes (merge or abandon
   them first); abandoning a parent abandons its open sub-changes and their branches. What its sub-changes
   merged is committed work of the parent (`recordInParent`, ADR 0103) before it is merged into main.
5. **Split**: `SplitByOwner` (`SplitChange` RPC) creates one sub-change per unit owning nodes the
   change has a candidate impact on (the owner of the pre version of a `modified` change impact with no draft
   yet, ADR 0024, 0054), below the unit holding the change. Since ADR 0103 the candidate moves to the sub-change: a
   copy derived from the parent's (`producedBy: graph.split_by_owner`, same rationale) is proposed there and the
   parent's is withdrawn; it comes back to the parent with the version the sub-change merges. It is idempotent per
   unit (a unit with an open sub-change is left as it is); impacts on nodes owned by the unit holding the change,
   or outside it, stay with the parent. Running an agent per sub-change uses the existing sub-agent mechanism on
   the sub-change (no engine builtin performs the split).

## Consequences
- Columns `change.parent_id`, `owner_org` (`0001_schema.sql` of both dialects).
- A dedicated organisation service is not needed: the graph, its namespace rules and a web editor
  are enough.
