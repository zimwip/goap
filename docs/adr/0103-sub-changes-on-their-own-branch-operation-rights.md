# ADR 0103 — A sub-change works on its own branch; the operations on a change are a catalogue the roles allow

**Status**: accepted, implemented; §1 amended by ADR 0105 (the parent's acceptances are one submitted review) and
extended by ADR 0106 (a root change attached as a sub-change, or detached; the guardian chain) · **Date**: 2026-10 · Supersedes ADR 0081 (sub-changes land in their parent) and ADR
0082 (rebase a sub-change onto its parent's drafts); amends ADR 0102 (a sub-change commits and merges like a root
change) and ADR 0043 (a methodology's roles also say which operations on a change they allow).

## Context

1. A sub-change had no branch: it saw its parent's drafts, copied them at its checkout (`Draft.Inherited`), handed its
   accepted drafts back to the parent (`integrateSub`) and was rebased against the parent's drafts. Two mechanisms lived
   side by side, one for a root change (a branch, commits, a rebase onto the head of the target, a fast-forward) and one
   for a sub-change, with their own exceptions in the checkout, the views, the rebase and the web.
2. A reviewer could do whatever a doer could: check a node out, edit a draft, withdraw an impact. Who may only accept or
   reject was not expressible; the roles of a methodology said who runs its agents and actions, not who performs which
   operation on its changes.

## Decision

### 1. A sub-change is a change on a branch forked from its parent's

- A sub-change is forked from the head of the branch of its parent change (the parent gets one if it has none yet),
  recursively: the sub-change of a sub-change forks from its parent's branch. It commits on its own branch
  (`CommitChange`, ADR 0102) and merges into its parent's like a root change into its target (`IntegrateChange` →
  `merge`): settled first, rebased onto the head of the parent's branch when it moved (`rebaseCommittedTx`), held by its
  own guardian (`checkMerge`), then a fast-forward of the parent's branch.
- A sub-change sees what its parent **committed**, never its drafts: a change with a branch of its own reads the version
  its branch holds (`ChangeNodeViewByKey`). A parent's uncommitted draft that modifies a node the sub-change changed holds
  the merge (`checkParentDrafts`): the parent commits (or drops) it first, so the work meets on branches only.
- The parent records the merged work as its own (`recordInParent`): for each node the sub-change landed, an impact of the
  parent (the existing one, else a new one `producedBy: graph.merge`, `derivedFrom` the sub-change's impact) gets an
  accepted review carrying the sub-change's last review ("merged from sub-change …"; since ADR 0105 all of them made by
  one submitted review of `graph.merge`) and a `landed` event naming the head of the parent's branch; the parent then has committed work to merge in turn. The work climbs one level per merge.
- Gone: `Draft.Inherited`, `inheritDraft`, `inheritedDrafts`, `integrateSub`, the rebase of a sub-change's drafts and its
  `integrated` events (old logs still read: the web's history keeps their edges).
- Web: the change overview gives a sub-change the same *Commit* and *Merge* buttons ("Merge into parent") as a root
  change; the sync pane (`SubChangeSync.svelte`) shows "Behind <parent's branch>" from `GetRebaseState`; the family
  history (`web/src/lib/subChangeHistory.ts`) starts a sub-change's lane where its parent stood, names its rebase and
  ties the parent's landing of a version to the sub-change's own (a "merged" edge).

### 2. The operations on a change are a catalogue generated from the code

- An operation is a method of `pkg/graph` that a person performs on a change, annotated with its code and its kind:

  ```go
  // ImpactNodeUpdate edits the draft of a change impact ...
  //
  //goap:operation impact.update doer
  func (g *Graph) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, ...) (cn domain.ChangeImpact, err error) {
  	if err := g.allow(ctx, id, operations.ImpactUpdate); err != nil {
  		return cn, err
  	}
  ```

  The kinds are `doer` (changes the work: checkout, create, update, transition, links, cancel, withdraw, merge / split,
  resolve, propose, commit, rebase, merge of the change) and `reviewer` (judges it: `impact.review`, also asked by the
  review batch, which declares nothing of its own).
- `cmd/goap-opgen` (logic in `internal/opgen`) reads the annotations and writes `pkg/operations/operations_gen.go`
  (constants and the catalogue: code, kind, method, first sentence of its doc). It refuses a malformed annotation, an
  unknown kind, a **code declared twice** or two codes giving the same constant, an annotation on anything but an
  exported method of `*Graph`, an annotated method that does not call `g.allow` with its own operation, and a `g.allow`
  naming an operation no annotation declares. `go generate ./pkg/operations/` (`make generate`) runs it;
  `internal/opgen.TestCatalogueIsCurrent` fails when the file is out of date. `pkg/operations` is a leaf package
  (`All`, `Lookup`, `Known`, `Expand`, `Allows`).
- The list is fixed by the code: a methodology names operations, it never defines one.

### 3. The roles of a methodology allow operations

- `methodology.Role.Operations` lists the operations a role allows, by code (`impact.review`) or by kind (`doer`,
  `reviewer`); the compiler refuses a name the catalogue does not know (`roles[i].operations[j]`). A role listing none
  allows every operation; a methodology none of whose roles lists any restricts nothing (the behaviour before this ADR).
  A reviewer is a role with `operations: [reviewer]`: it accepts or rejects, and never checks out, edits or withdraws.
- `Methodology.OperationRoles(code)` gives the roles allowing an operation; the registry serves it from the stored
  definition (`registrysvc.Service.OperationRoles`).
- The graph asks `Graph.OperationAuthorizer` (nil: everything allowed) before each operation, outside any transaction
  (`g.allow`). `graphsvc.OperationAuthorizer(authorizer, roles)` checks `operation:perform` on the change's project with
  the allowed roles (`authz.Resource.Roles`, the default policy `mayRun(r.sub, r.obj)`: one of them held there, through
  an Assignment, ADR 0043); an operation no role allows is an administrator's; a platform service acting by itself
  (`authz.Principal.System`) is not asked. `goap-dev` wires it from the registry in process; `cmd/graph`, which has no
  registry, does not (nothing restricted).
- Proto `registry.v1.Role.operations`, attribute `operations` of `methodology@Role`, web: the Roles pane of the
  methodology editor edits them (comma separated).

## Consequences

- One path for every change: the special cases of sub-changes are gone from the checkout, the views, the rebase, the
  commit and the web.
- A sub-change no longer sees what its parent is drafting; parent and sub-change communicate by committing, merging and
  rebasing.
- Who may do what on a change is said by the methodology, from a fixed list the code owns and the generator keeps free of
  duplicates; a new operation is one annotation, one `g.allow` call and `go generate`.
- The default policy `operation:perform` is seeded on a new install (`graphsvc.SeedAccess` seeds the defaults once): an
  install seeded before this ADR adds that Policy node (or restricted methodologies are reserved to administrators).
- Not done: the engine's builtin actions and the assistant do not name the operations; the web does not yet hide the
  buttons a caller may not use (the graph refuses them).
