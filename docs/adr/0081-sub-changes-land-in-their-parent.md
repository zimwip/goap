# ADR 0081 — A sub-change lands in its parent change, not on its branch

**Status**: superseded by [ADR 0103](0103-sub-changes-on-their-own-branch-operation-rights.md) (a sub-change works on its own
branch, forked from its parent's, commits and merges into it like a root change); was accepted, implemented (graph), its §5
(resolution at integration) amended by ADR 0082 (itself superseded by ADR 0103) and its §3 by ADR 0102 (a sub-change did
not commit) · **Date**: 2026-10 · Superseded, while it stood, ADR 0016 §3 and §4 (a sub-change forks its own branch from
the parent branch and is merged into it), which ADR 0103 brought back in a new form; built on ADR 0029 / 0030 (one
event-sourced log per change), ADR 0079 (drafts, versions at landing) and the adoption of a flow (ADR 0025 §5, ADR 0079 §5).

## Context

Since ADR 0079 a change writes no version while it works: what it holds of a node is a draft, the fold of its impact log, and
the versions are written when the change lands. A sub-change (ADR 0016) still worked with branches: it forked its own branch
from the head of its parent's branch and, when applied, wrote its versions and merged them into that branch. The head of the
parent's branch did not move while the parent worked (its work was drafts), so a sub-change did not see what its parent was
doing, its landing fast-forwarded into the parent's branch whatever the parent held a draft of (the parent's own landing then
met a conflict, or lost its edit), and the parent's log said nothing of the work its sub-changes brought in.

## Decision (as it stood)

The parent change *is* its log, so landing a sub-change in its parent was writing that work into the parent's log, the way an
adopted flow installs its drafts on the main flow:

1. A sub-change **saw its parent's drafts** (`Graph.inheritedDrafts`, up the chain of parents), its reference baseline
   staying the head of its parent's branch.
2. A **checkout copied the parent's draft** and recorded where it came from (`Draft.Inherited`: change, impact and the `seq`
   of the last event of that impact); a node the parent created was taken with an impact of intent `created` too.
3. **Committing a sub-change wrote no version**: it ran the rules of a landing on a blackboard built from its drafts
   (`subLandingBlackboard`), the landable states left to the parent, and left the sub-change `committed` with no baseline.
4. **Integrating it wrote its drafts into its parent's log** (`integrateSub`): a `transitioned` (or `proposed` /
   `checkedOut`, or `created`) event carrying the draft and `Patch {"integrated": …}`, an impact `ProducedBy`
   `graph.integrate` when the parent held none, a `reviewed` event carrying the sub-change's review over, and an
   `integrated` event per impact in the sub-change's log (`ImpactEvent.Into`).
5. **Conflicts were read in the parent's log** (an event on the parent's draft after `Inherited.Seq`, a draft the parent took
   since, an impact it rejected), settled by a `Resolution` (`Skip`, `Props`) at `IntegrateChange`.
6. The parent landed as any change: every version of its sub-changes' work was written once, by the parent.

## Since then

ADR 0082 replaced the resolution of §5 by a rebase of the sub-change onto its parent's drafts; ADR 0102 made a sub-change
not commit at all (its merge installed its accepted drafts). ADR 0103 replaced the whole mechanism: a sub-change is forked
from the head of its parent change's branch (`prepareSubChange`; the parent gets a branch), sees what its parent committed and
never its drafts, commits on its own branch and merges into its parent's (`IntegrateChange` → `merge`: `unsettled`,
`checkParentDrafts`, rebase onto the parent's head, `checkMerge`, fast-forward, then `recordInParent`: an accepted impact of
the parent and a `landed` event at its head per node). `Draft.Inherited`, `inheritDraft`, `integrateSub` and
`subLandingBlackboard` are gone; `domain.ImpactIntegrated` and the `integrated` events of old logs are still read (the web's
history).
