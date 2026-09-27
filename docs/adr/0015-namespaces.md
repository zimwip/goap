# ADR 0015 — Namespaces and namespace-scoped changes

**Status**: accepted, implemented · **Date**: 2026-09

## Context
Node keys were globally unique free strings and a change could touch any node. We want to organise the graph by
**namespace**, make a change act on exactly one namespace, and let nodes of one namespace reference another (a
delivery node owned by an organisation unit).

## Decision
1. Every node lives in one **namespace** (`node.namespace`); keys are unique per `(namespace, key)`.
   **A namespace is the content of one domain** (ADR 0013): its name is the domain's, and it is the prefix of the
   type of its nodes (`alm@Requirement` lives in `alm`, ADR 0012). Namespaces in use: `alm` (delivery),
   `organisation` (units, adapters, users, policies), `platform` (MCPs, adapter definitions, model configuration),
   and the meta-domains `methodology` and `domain` (the definitions, ADR 0023). There is no default namespace.
2. A **change** has a namespace (`change.namespace`). It may create nodes in that namespace only and modify (write,
   transition, merge, add outgoing links from) only nodes of that namespace; the change impacts enforce it when a
   version is written (ADR 0024).
3. **Cross-namespace references** are allowed: impacts and link targets may point
   at nodes of another namespace.
4. `authz.Resource` carries the namespace so policies can restrict who may act on which namespace.

5. **Change branch** (every change has one, ADR 0024): the change
   forks a branch named `change-<id8>` (origin `change:<id>`) from its reference baseline; its versions
   live there. `Apply` accepts the change impacts on that branch, then merges the branch into the branch it
   was forked from (`Branch`, `main` by default) with the ADR 0009 3-way merge. Without conflicts the
   change becomes `applied` and its result baseline is the merge baseline. With conflicts it becomes
   `merge_pending`: a human resolves them with `MergeChange` (per-node resolutions), which completes the
   merge. Abandoning the change abandons its branch.
6. **Shared versions**: changes acting on the same node are detected through the attachments
   (`SharedNodes` / `GetSharedNodes`). Each change keeps its own version on its own branch; the second
   one to be applied meets the first one's version on the target branch and goes through the 3-way
   merge (auto-merged when properties are disjoint, `merge_pending` otherwise). Building one change
   on the unmerged versions of another is not supported yet.
7. `NodeByKeyOn(namespace, branch, key)` resolves a key as seen from a branch (own version, else the
   parent's, up to main).

8. **Pre/post impacts**: a change impact carries the version a change starts from (pre), the version it writes
   (post) and the version that lands on the target branch (ADR 0024).
9. **Maturity**: the `maturity` lifecycle of `domains/alm.yaml` (proposed, draft, accepted, released,
   withdrawn) is a reusable template for a type (`lifecycle: maturity`), on top of ADR 0014.

10. **Blackboard steps**: the change is the blackboard of an intent (`change.intent`, recorded on
    `process.started`). Each step of an agent run is journaled (ADR 0011) as a transition of the
    blackboard: `boardBefore` / `boardAfter` (item count of the change when the step starts / ends),
    `reads` (node versions referenced on the blackboard when it started), and `items` (the proposals
    it produced). For a single process the marks chain: a step starts from the board its predecessor
    left. The marks are counts, so items added by concurrent processes also move them.

11. **Sub-changes and organisation**: see [ADR 0016](0016-organisation-and-sub-changes.md).

## Consequences
- `Graph.NodeByKey` and every write take a namespace; a change names its namespace (CLAUDE.md, rule 2).
- The delivery data lives in `alm`, not in a default namespace; graphs created before are reset (ADR 0012).
