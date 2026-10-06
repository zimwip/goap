# ADR 0081 — A sub-change lands in its parent change, not on its branch

**Status**: accepted, implemented (graph); §5 (resolution at integration) amended by ADR 0082 (rebase, fast-forward
integration) · **Date**: 2026-10 · Supersedes ADR 0016 §3 and §4 (a sub-change forks its own
branch from the parent branch and is merged into it; the parent starts from the head of its own branch) and the sentence of
ADR 0079 §4 "a node that changed on the change's branch since the draft was checked out (a sub-change merged a version) is a
conflict"; builds on ADR 0029 / 0030 (one event-sourced log per change), ADR 0079 (drafts, versions at landing) and the
adoption of a flow (ADR 0025 §5, ADR 0079 §5).

## Context

Since ADR 0079 a change writes no version while it works: what it holds of a node is a draft, the fold of its impact log, and
the versions are written when the change lands. A sub-change (ADR 0016) still worked with branches: it forked its own branch
from the head of its parent's branch and, when applied, wrote its versions and merged them into that branch (fast-forward
or three-way merge). The head of the parent's branch no longer moves while the parent works (its work is drafts), so:

- a sub-change did not see what its parent was doing: it started from the parent's fork baseline, not from the parent's
  drafts;
- its landing fast-forwarded into the parent's branch without conflict, whatever the parent held a draft of; the parent's
  own landing then met a node "changed on the branch of the change since it was checked out", or, before that check, lost
  its own edit silently;
- the parent's log said nothing of the work its sub-changes brought in: the versions appeared on its branch, beside the log.

The parent change *is* its log: landing a sub-change in its parent is writing that work into the parent's log, the way an
adopted flow installs its drafts on the main flow.

## Decision

### 1. A sub-change sees its parent's drafts

A sub-change reads the nodes of its parent as the parent's main flow sees them: what the sub-change sees of a node is its own
draft (on its flow chain, ADR 0025), else the draft the parent holds of it on its main flow (else the grandparent's, up the
chain of parents: `Graph.inheritedDrafts`), else the stored version of its reference baseline. The reads of a change (the
draft reader behind `ChangeNodeView`, the blackboard, `ChangeGraph`; `ChangeView`, whose nodes include the parent's drafts as
draft references) see the inherited drafts; a link of a draft of the sub-change may target the draft of a node its parent
holds.

The reference baseline of a sub-change stays the head of its parent's branch, which is the parent's fork baseline while the
parent works.

### 2. A checkout copies the parent's draft, and remembers where it came from

`ImpactNodeCheckout` (and the checkout `ImpactNodeTransition` makes for itself) of a node the sub-change does not hold a draft of
on its flow chain copies the parent's draft when the parent holds one: a fork of it (links with ids of their own), as a flow
copies the draft of its parent flow. The draft records its origin, `Draft.Inherited`: the change and the change impact it was
copied from and the `seq` of the last event of that impact in that change's log when it was copied.

A node the parent **creates** has no stored version and is in no baseline: the sub-change takes it with an impact of intent
`created` too (the same node id, no pre version), recorded by one `created` event whose draft is the copy of the parent's
(ADR 0077: a new node is never proposed). Creating a node whose key a parent holds a draft of is a conflict.

### 3. Committing a sub-change writes no version

`CommitChange` (so `Apply`) of a sub-change runs the rules of a landing without writing anything to a branch: no open flow,
no pending decision point, no open sub-change of its own, every change impact reviewed, every accepted draft checked as the
version written from it would be (`checkDraft`: attributes, validators, required links, link attributes) and its origins
accepted (ADR 0077), the final state of the change's lifecycle (ADR 0058) and the landing gate when it decides (ADR 0066), asked on a blackboard
built from the drafts (`subLandingBlackboard`: the pre version and the draft of each impact, as the sub-change sees them). The
states a node is left in are not checked for landing (ADR 0078): the sub-change hands its drafts to the parent, which may go on
working on them, and the parent's landing checks them. The sub-change is then `committed`; it has no commit baseline
(`ResultBaselineID` stays empty).

### 4. Integrating a sub-change writes its drafts into its parent's log

`IntegrateChange` (so `Apply`) of a committed sub-change installs, in one transaction, every accepted draft of its main flow
in the parent's main flow, by events of the parent's log, and records it in the sub-change's log:

- the parent holds a change impact of the node on its main flow: a `transitioned` event carrying the draft (`Draft`) and
  `Patch {"integrated": {"change", "impact"}}` installs it, as an adoption does;
- it holds none: the parent gets an impact of the same intent, derived from the sub-change's (`DerivedFrom` = its id,
  `ProducedBy` `graph.integrate`): a `proposed` event then a `checkedOut` event carrying the draft for a modified node, one
  `created` event for a created one (the same node id), with the same patch;
- then a `reviewed` event accepts it in the parent: the review is the sub-change's, carried over (`By` the sub-change's last
  reviewer, the comment "integrated from sub-change <title> (<id>)" followed by that reviewer's comment, its `ReviewID`); the
  `ReviewPolicy` is not asked again, the graph writes it as the result of a review already made;
- in the sub-change's log, an `integrated` event per impact (`ImpactEvent.Into`: the parent change and impact) drops its draft.

An installed draft keeps its `Inherited` when it names an ancestor of the parent (the parent integrates it in turn), and loses
it when it names the parent itself. Links keep their targets: a draft reference of a node of the parent is the parent's draft,
resolved to the version written when the parent lands, as any link of a draft (ADR 0079 §4).

The sub-change is then `applied` and its branch (which received nothing) is closed as merged.

### 5. Conflicts are read in the parent's log

A draft of the sub-change conflicts with its parent when the parent changed the node since the sub-change took it:

- the draft was copied from the parent (`Inherited` names the parent): an event that changes the parent's draft of that impact
  (created, checkedOut, updated, transitioned, cancelled) was written on the parent's main flow after `Inherited.Seq`;
- the draft was taken from the stored version, and the parent holds a draft of the node now;
- the parent rejected its impact of the node.

The integration of a sub-change of another sibling is such an event: two sub-changes editing one node conflict when the second
integrates. Conflicts follow ADR 0016's rule for a merge: `Apply` leaves the sub-change `committed` (its integration waits),
`IntegrateChange` refuses while a conflicting node has no `Resolution`: `Skip` leaves the node out (the sub-change's impact is
recorded `integrated` with no install), `Props` installs the sub-change's draft with those properties (the resolved
properties, whole).

### 6. The parent lands as any change

The parent's landing (ADR 0079 §4) writes the versions of its drafts, the integrated ones among them: every version of the
work of its sub-changes is written once, by the change that lands on a branch, and its history reads `integrated` in the log of
the parent. Nothing is merged into the parent's branch, so the check "the node changed on the branch since it was checked out"
no longer has a sub-change cause; it stays for a branch moved by other means. `SplitByOwner` is unchanged: the parent's impact
of a delegated node is accepted ("delegated to …") and the sub-change's work comes back into it by the integration (§4: the
parent holds the impact, its draft is installed).

## Consequences

- A parent and its sub-changes edit one node without losing a write: the sub-change starts from the parent's draft, and a
  parent that moved on meanwhile is a conflict, read on the log.
- A sub-change writes no version and no baseline: the history of a node has one version per change that landed on a branch;
  the parent's log tells every integration, the sub-change's log where each impact went.
- `domain.ImpactIntegrated`, `ImpactEvent.Into` (`domain.ImpactRef`) and `Draft.Inherited` (`domain.DraftOrigin`) are new; the
  schema is unchanged (both travel in the JSON of the log).
- Not done: an integrated sub-change's own reads after integration (its impacts keep a draft reference whose draft is gone: the
  work is read in the parent); a three-way merge of properties for a conflict (a resolution gives the properties whole).
