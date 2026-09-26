# ADR 0017 — Flow branches: relaunching a step of the action flow

**Status**: accepted · **Date**: 2026-09 · Extends ADR 0001 (blackboard), ADR 0011 (journal), ADR 0015 (step marks).

## Context
Relaunching a step of an agent run may invalidate its previous output and every conclusion
drawn from it. The run must be replayed from that point, the previous outputs must stay
auditable, and nothing may be replaced before a human agrees.

## Decision
The blackboard of a change is an **append-only log** of items (event sourcing); nothing is
rewritten. Relaunching is a **flow branch**, and every transition is an event of the same log.

1. **Events**: items of kind `flow` carry a `FlowEvent`: `open` (parent flow, the last item before the
   step, the step and journal record restarted, the relaunching process, the reason, the items
   invalidated), `adopt`, `discard`. The state of the branches is a replay of these events
   (`domain.ChangeSet.Flows`). Items carry the flow that produced them (`flow`, empty = main flow).
2. **Starting point of a step**: a step records `boardLast`, the last item of its flow when it started
   (with `boardBefore` / `boardAfter`, ADR 0015), so a relaunch knows where to fork.
3. **Stale marking (automatic)**: the seeds are the items produced by the relaunched step, by the
   steps after it and by the sub-agent runs started from it (found through the journal). The closure
   over `derivedFrom`, links to an item, decisions and impact posts (`domain.StaleClosure`) is the
   stale set, stored in the `open` event. While the branch is open those items have the effective
   status `stale`: they still count, with a warning.
4. **Candidates**: the replanned run appends its items to the branch. Their effective status is
   `candidate`: they do not count (apply, lifecycle replay, conditions of the main flow ignore them).
5. **Views**: the process on a branch reads `View(flow)` = the parent's view without the stale items
   plus its own items (`GetBlackboard(change, flow)`), so it replans from the state before the step;
   independent items produced meanwhile are kept. The main flow reads the log without the items of
   branches that are not adopted.
6. **Replan from K**: `Engine.Relaunch(process, step, reason)` opens the branch and returns a new
   process (`flow`, `relaunchOf`, `fromStep`) that runs the normal observe/plan/act loop. When it
   reaches its goal it waits with a human task of kind `flow` instead of completing.
7. **Human confirmation**: `Engine.DecideFlow(process, adopt)`. Adopting (only once the goal is
   reached): the stale items become `superseded`, the candidates count, the replaced process becomes
   `superseded`. Discarding (possible while the branch is open): the candidates are `rejected`, the
   stale items count again, the relaunched process is `superseded`, the previous run is untouched.
   Both are journaled (`approval` record, action `flow`).
8. **Guards**: a change with an open branch cannot be applied; a batch of items belongs to one flow;
   only processes without a parent can be relaunched (relaunch the parent step, its sub-agent outputs
   are seeds).

## Parallel branches, guidance and the domain branch (amendment)
- **Parallel branches**: a change may have several open flow branches (alternatives). Each reads its
  own view (the parent's view without the items it invalidated, plus its own items), so the
  alternatives do not see each other. They are decided one by one. An open flow **competes** with a
  flow adopted after it was opened when they replace the same items, or when its candidates derive
  from items the adopted flow replaced (`Flow.CompetesWith`): adopting a competing flow is refused, it
  is relaunched or discarded. Independent branches are adopted freely.
- **Guidance**: relaunching takes an optional comment (`guidance`). It is recorded as an item of type
  `guidance` on the branch, so only the relaunched steps read it: LLM actions get it appended to their
  prompt ("Guidance from a human reviewer..."). A board-inconsistency relaunch passes the human's
  comment the same way.
- **Domain branch**: the proposals of a flow view (kept and candidate ones) are applied on a graph
  branch of its own (`flow-<id>`, forked from the branch the change acts on) by `MaterializeFlow`, done
  by the engine when the relaunched run reaches its goal, so the reviewer can read the resulting graph
  (`PlanMerge` of the flow branch against the change branch) before deciding. Nothing is written when it
  fails. Adopting merges the flow branch into the change branch **when the change has a branch of its
  own** (a change acting directly on main does not publish before it is applied) and the merge has no
  conflict; the proposals merged this way are recorded on the adopt event and are not applied a second
  time when the change is applied (`ChangeSet.MergedOnBranch`, skipped by the applier and by the
  divergence check). Discarding abandons every branch the flow used. If the flow changed since it was
  materialized, adoption does not merge: the proposals are applied with the change as usual.

## Blackboard validation before each action
The engine validates the blackboard the process reads at the start of every cycle, before planning
(so before any action is asked, and before the goal is declared reached): `Graph.ValidateBoard`.

- **Checks**: item structure; provenance and references to items that do not exist (`dangling`);
  items built on an item that is rejected or superseded (`derived_from_invalid`, the culprit is the
  parent); nodes outside the reference baseline (`reference`); proposals based on a node version that
  has moved (`outdated`); lifecycle, namespace and link rules, located per proposal (`rule`); impact
  pre/post coherence (`impact`); a node created twice (`duplicate`). Each issue names the item where
  it shows and the **culprit** item to blame.
- **Where to restart**: the culprit items are mapped to the steps that produced them (`execution`),
  a sub-agent step to the step of its parent that started it. The earliest step over every run of the
  change is the proposal. None is proposed when the content did not come from a step (human,
  trigger), or when a flow is already open.
- **Human decision**: the process waits with a `board` task (issues and proposal).
  `ResolveBoard(relaunch)` opens the flow branch from the proposed step and the process waits
  (`relaunched`) for the decision on that flow, then resumes by itself (adopted: the board is fixed;
  discarded: it goes on with the issues ignored). `ResolveBoard(ignore)` resumes at once. A set of
  issues that was ignored is not asked again until it changes.
- A validation failure (graph unreachable) never blocks a process.

## Consequences
- No schema change: events are ordinary change items. Readers of item statuses use
  `ChangeSet.InEffect`; the CEL `items` of the main flow no longer contain candidates.
- Permissions: the actions `relaunch` and `decide_flow` on the `process` resource (same roles as the
  other process actions).
- Node versions proposed by an abandoned branch are only proposals until apply: no rollback needed.
