# ADR 0077 — Naming of the change-driven node operations

**Status**: accepted, implemented · **Date**: 2026-10 · Amends ADR 0076 (checkout, working versions, check-in), refines
ADR 0024 / 0029 (change impacts, their event log).

## Context

ADR 0076 made every node write an operation of a change, but its names came from three vocabularies: the node (`CreateNode`,
`UpdateNode`), the version control (`CheckoutNode`, `CancelCheckout`) and the change impact (`AddChangeImpacts`,
`ReviewChangeImpact`, `RemoveChangeImpact`). Nothing in a name said that the operation belongs to a change, and `CreateNode` or
`UpdateNode` read like the direct writes ADR 0049 closed. The impact log used verbs of the old model (`declared`, `written`,
`removed`) that no longer matched the operations emitting them.

## Decision

One rule: an operation of a change on its impacts carries the word **Impact**.

- Operations on the node an impact is about: `ImpactNode<Verb>`: `ImpactNodeCreate`, `ImpactNodeCheckout`, `ImpactNodeUpdate`,
  `ImpactNodeTransition`, `ImpactNodeCancel` (drops a working version), `ImpactNodeReview` (and
  `ImpactNodeReviewOn`, the variant that names the flow and the execution).
- Operations on the outgoing links of a working version: `ImpactLink<Verb>`: `ImpactLinkCreate`, `ImpactLinkUpdate`,
  `ImpactLinkDelete`.
- Operations on the impact itself: `ProposeImpact` (declares the impact of an **existing** node without editing it,
  formerly `AddNodes` / `AddChangeImpacts`) and `WithdrawImpact` (formerly `RemoveChangeImpact`).

**A proposal is for existing nodes only.** `ProposeImpact` takes impacts of intent `modified` (their `pre` in the reference
baseline) and refuses `created` (`ErrInvalid`: "a new node is created with ImpactNodeCreate; ProposeImpact is for existing
nodes"). A new node is purely created: there is no plan of a creation, no impact without a version for it.

The names are the same in the graph (`Graph` methods), the service (RPCs and messages of `graph.v1`, `graphsvc.Client`),
the engine port (`engine.GraphPort`), the DSL (`ctx.impactNodeCreate`, `ctx.impactNodeReview`,
`ctx.impactNodeReviewWithReserve`, `ctx.impactNodeTransition`, `ctx.impactNodeCancel`,
`ctx.withdrawImpact`) and the web client (`graph.impactNodeCreate`, ...). The input structs (`NodeCreate`, `NodeCheckout`,
`NodeUpdate`, `NodeTransition`) keep their names.

The **event ops** of the impact log follow the verb of the operation that writes them: `ImpactDeclared` `declared` becomes
`ImpactProposed` `proposed` (the impact of an existing node, added without a version); `ImpactWritten` `written` becomes `ImpactTransitioned` `transitioned` (the version written by
a transition, a merge or an adoption: the operations that write a version outside the checkout / update
cycle); `ImpactRemoved` `removed` becomes `ImpactWithdrawn` `withdrawn`. The others are unchanged: `created`, `checkedOut`,
`updated`, `cancelled`, `reviewed`, `discarded`, `adopted`, `landed`, `rebased`. Log entry types follow
(`impact.proposed`, `impact.transitioned`, `impact.withdrawn`).

**Event model.** `proposed` adds the impact of an existing node (`State`, intent `modified`). `created` is the creation of a
node: **one event** carries both the impact (`State`, intent `created`, as `proposed` does) and its first version (`Post`),
so the log of a creation reads `created`, then `updated` / `reviewed`..., never `proposed` first and never a
`checkedOut` (the creation is born checked out). `checkedOut` is only the checkout of an existing node's next version. A
fold adds the impact on `created` like on `proposed`, then sets its post; the PROV-O export maps a `created` event to both
the impact activity and the version entity; the merge change's own events follow the same rule (a node it creates on the
target is one `created` event, then `reviewed`, `landed`).

## Resolution

Every operation that names a node resolves it through one function (`resolve`, `pkg/graph/checkout.go`) against the
reference baseline of the change and what the flow sees of it; an operation names a change impact, or a node (its id, or
its key, with the type for a creation):

- a change impact id is taken as it is;
- a node the flow already sees in the change reuses its impact: a node appears once per flow, never doubled;
- a node of the reference baseline gets, when the change holds none on the flow, an impact `modified` (`pre` the baseline
  version) proposed in the same transaction, with the rationale of the call (`proposed` event);
- `ImpactNodeCreate` needs a key held neither by the baseline (`ErrConflict`, "the reference baseline already holds it") nor
  by the change (`ErrConflict`, "the change already holds it as change impact ..."); the impact is checked in the
  resolver but recorded only by the `created` event of its first version (one transaction, one event). There is no planned
  creation to fulfil;
- any other operation (`ImpactNodeCheckout`, `ImpactNodeUpdate`, `ImpactNodeTransition`) on a node in neither the baseline
  nor the change is `ErrNotFound` ("in neither the reference baseline nor the change"), and a refused operation declares
  nothing (one transaction).

`ImpactNodeUpdate` accepts a node (`NodeUpdate.Node` / `Key`) in place of the impact id; as it edits a working version, an
update of a node not checked out is still refused. The link operations name a link, not a node, and keep finding the
impact by it.

## Merge and split

A node never disappears, and a merge or a split retires nothing (ADR 0076 §3, §4c). Both are seen **from the parent
side**: merging A and B into C is a modification of the parent P of A and B, whose working version loses the links
P→A and P→B and gains P→C; splitting A into C and D loses P→A and gains P→C and P→D. A, B and the old versions of P stay
in history untouched.

- **Operations**: `ImpactNodeMerge` (`MergeInput{Sources, Into}`) and `ImpactNodeSplit` (`SplitInput{Source, Into}`),
  in `pkg/graph/restructure.go`; names are the same in the service (RPC `ImpactNodeMerge` / `ImpactNodeSplit`,
  `graphsvc.Client`), the engine port, the DSL (`ctx.impactNodeMerge(sources, type, key, rationale)`,
  `ctx.impactNodeSplit(source, [{type, key, rationale, props}])`), the `goap-change` tools `merge` / `split` and the web
  client. Each is one transaction built on the resolver of the previous section. The result lists the successors, the
  sources, the nodes whose links moved and the suspect links.
- **Parent**: the node holding a link of a type flagged `compose: true` to the source (any version of the source; only
  the version of the holder the flow sees counts). A source with several parents is one modification per parent (the
  impact of a parent the change already holds is reused). A source with no parent is refused (`ErrInvalid`), as are a
  source created by the change, one of another namespace or of a type incompatible with the successor (same type, or one
  a subtype of the other), a source that is the parent of another, and a source checked out (`ErrConflict`). A refused call
  declares nothing.
- **Other links**: a link of another type to a merged source follows it (the holder is modified: its link is replaced by
  one to the successor); a link of another type to a split source is left as it is and returned as **suspect** (ADR 0003:
  which successor it should follow is for the reviewer). The outgoing links of the sources are not copied: the caller
  gives the successor its links (`Into.Links`).
- **`via`**: the impacts of the sources (declared by the call, or re-proposed when the change held them without a `via`)
  carry the impact of their parent that realizes the move, the first parent in key order when there are several.
- **Lineage**: the successor's first version has `Origins []NodeRef` (the versions of the sources the call saw): pure
  lineage across nodes, never structural (`Parents` are the versions of one node). It is a column of `node_version`
  (`origins`, JSON, both dialects); `Graph.DerivedNodes(ref)` (RPC `DerivedNodes`, one query in `sqlbuild.go`) answers
  "which nodes derive from A" (`Version` 0: any version), `Node.Origins` the other way. The `created` event carries
  `patch.origins` (`{id, version, key}`), which the audit trail reads ("merged from A, B" / "split from A") and the PROV-O
  export maps to `prov:wasDerivedFrom` of the successor's version.
- **Review gate**: a node created with origins is accepted and landed only when the change impact of every
  origin is accepted (`ErrConflict`, checked by `ImpactNodeReviewOn` and the landing, `checkOrigins`).
  The sources, the parents and the successors land in the one baseline of the change, atomically.
- **Authorization**: like `ImpactNodeCreate` for the types created, and the gate of the access nodes (`adminOnly`) stands
  in front of every node the call modifies, the parents it discovers included (`MergeInput.Gate`, called inside the
  transaction by the handler and the `goap-change` connector; the engine's DSL does not gate, as for a creation).

Not done: merging into a node that already exists (a successor is always new: origins are set on a first version);
copying or reconciling the outgoing links of the sources; a source with several parents has a single `via`; a `via`
already set to another parent's impact is a conflict, not rewritten; no web editor (the audit shows the lineage, the web
client has the calls).

## Consequences

- A pure rename, plus the rule above: no behaviour changes for existing nodes; a creation can no longer be planned by
  `ProposeImpact`, and logs one `created` event instead of `proposed` then `created` / `checkedOut`. As the project is greenfield there are no aliases; existing development databases
  hold `impact.declared` / `impact.written` / `impact.removed` entries and are reset.
- The `goap-change` MCP tools (`write`, `edit`, `link`, `unlink`, `cancel`, `remove`) keep their agent-facing names (`checkin` goes, see below).
- Older ADRs keep the names of their time.

## No explicit check-in: a working version stays one until the change lands

ADR 0076 had a check-in (`ImpactNodeCheckin`): an accepted review authorized it, it froze the working version, and
editing the node again was a new checkout that wrote another version. A user's flow showed the cost: propose a node,
check it out, edit, auto-accept, edit again produced v2 then v3, where one version and two `updated` lines were expected.
The check-in is removed, with its RPC, the `checkedIn` event, `ctx.impactNodeCheckin`, the `goap-change` `checkin` tool,
the `graph.apply` check-in loop and the web's `checkinAccepted`.

- **A version the change writes is a working version until the change lands.** Landing (`CommitChange`, so `Apply`)
  freezes it (`checked_out` false) in the commit transaction, with the other versions of the node the change wrote and
  left working (a version a parent flow derived from), re-runs the frozen-state checks (`checkFrozen`: validators,
  required links, link attributes, the origins gate) and requires every impact accepted. The node index hears of the
  frozen version then (`Graph.Observe`), never earlier.
- **Accepting is a gate, not a freeze.** `ImpactNodeReviewOn` with an acceptance runs `checkFrozen` and the origins gate
  on the working version: a failure (`ErrInvalid` / `ErrConflict`, the validator's message) refuses the acceptance, the
  review stays `proposed` and the version is unchanged. Success only records `reviewed`.
- **Editing after an acceptance is in place.** `ImpactNodeUpdate` and the `ImpactLink*` operations edit the working
  version whatever the review (a rejected impact is still refused); when the review was `accepted` they emit, after the
  `updated` event, a `reviewed` event back to `proposed` ("edited again"). The log of the flow above reads `proposed`,
  `checkedOut` (v2), `updated`, `reviewed` (accepted), `updated`, `reviewed` (proposed), `reviewed` (accepted): one
  version.
- **`ImpactNodeCheckout` is refused (`ErrConflict`, "already checked out") while the impact holds a working version of
  its flow**; callers (web, engine, DSL, `goap-change`) do not check out then, they update (`graph.IsWorking(change, flow,
  version)`: checked out and written on the branch the flow writes on). A flow whose parent flow holds a working version
  checks the node out to write its own; adopting a flow refuses a working version the flow did not accept.
- **A transition on a working version is taken in place.** `ImpactNodeTransition` on a node whose version is a working
  version of the change sets the state of that version (`Tx.SetNodeState`) and emits `transitioned` with the patch
  `{"state": {"from", "to"}}` and the working version as `post`: no version is written, and the phase of the impact
  (ADR 0058, "leaving a state freezes the impacts written in it") is untouched, as an in-place `updated` is
  (`ImpactEvent.WritesVersion`: false for it). Permission (`Graph.Authorizer`) and CEL guard (which sees `impact`) are
  checked when it is taken, as before. It does **not** send an accepted review back to `proposed`: a guard such as
  `impact.review == "accepted"` reads the acceptance and the move is what it authorizes (the requirement `approve` of
  the ALM domain, taken by the SDLC after the review). On a node with no working version in the change (a baseline or
  landed version, or the frozen version an earlier transition wrote) a transition keeps writing a version of its own,
  frozen at once, event `transitioned` without patch. At landing, `walkTransitions` follows the in-place moves recorded
  in the log of the change that wrote each version, so a creation moved `proposed` → `draft` → `approved` in place is one
  version whose chain of moves is checked.
- **A user is one version.** `EnsureUser` creates the `User` and activates it in one commit (`Graph.Commit`: a node with a
  working version is moved to its state first, then accepted): the log reads `created`, `transitioned`, `reviewed`,
  `landed`, the node has version 1 in state `active`.
- Unchanged: `ImpactNodeCancel`, `WithdrawImpact` (refused once a frozen version of the impact exists, which a transition
  of a node without working version writes), the merge and split origins gate, review after landing.
