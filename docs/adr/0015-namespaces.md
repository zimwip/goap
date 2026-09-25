# ADR 0015 — Namespaces and namespace-scoped changes

**Status**: accepted (implemented) · **Date**: 2026-09

## Context
Node keys were globally unique free strings and the meta model was only a key-prefix convention
(`M:<methodology>/…`, `D:<domain>/…`). A change could touch any node. We want to organise the graph
by **namespace** (`metadata`, `sdlc`, `organisation`, …), make a change act on exactly one namespace,
and let nodes of one namespace reference another (a domain node owned by an organisation unit).

## Decision
1. Every node lives in one **namespace** (`node.namespace`); keys are unique per `(namespace, key)`.
   Default namespace: `sdlc`. `metadata` is the meta model bound to the objects the code manipulates
   (methodology / domain projection, `pkg/metamodel`); its `M:`/`D:` key prefixes are kept.
2. A **change** has a namespace (`change_set.namespace`). It may create nodes in that namespace only
   and modify (update, delete, transition, merge, add outgoing links from) only nodes of that namespace
   (`pkg/graph/namespace.go`, enforced by `walk`, hence by `AddItems` and `Apply`).
3. **Cross-namespace references** are allowed: impacts, link targets and `instanceOf` links may point
   at nodes of another namespace. `instanceOf` (data node → NodeType) is code-managed and exempt from
   the source-namespace rule.
4. `authz.Resource` carries the namespace so policies can restrict who may act on which namespace.

5. **Change branch** (opt-in with `NewChange.OwnBranch` / `CreateChangeRequest.own_branch`): the change
   forks a branch named `change-<id8>` (origin `change:<id>`) from its reference baseline; its versions
   live there. `Apply` applies the proposals on that branch, then merges the branch into the branch it
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

8. **Pre/post impacts**: an `impact` item carries `target` (pre: a version of the reference baseline;
   when its type has a lifecycle it must be in a non-editable state, so a released version) and
   `post` (an endpoint: the proposal that produces the new version, or that version once known). The
   post proposal must act on the pre node (or create the node when there is no pre); checked in
   `AddItems`, in any order within a batch. `Impacts` / `GetImpacts` give the resolved view with the
   maturity state of both sides (the post version is known once the change has applied on its branch).
   Existing impacts with only a `target` keep working. The engine does not create the pair
   automatically (it would change the impacts that methodology conditions count): a producer names
   it with `post` in the item input, and CEL items expose `post`.
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
- Migration `0006_namespace` (PostgreSQL) / `0005_namespace` (SQLite, table rebuilt): existing `M:`/`D:`
  nodes go to `metadata`, everything else to `sdlc`.
- `Graph.NodeByKey` and `metamodel.CreateObject` take a namespace.
