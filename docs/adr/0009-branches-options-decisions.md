# ADR 0009 — Version branches, analysis options, decision loops, and merge

**Status**: accepted, mostly implemented (see Implementation status) · **Date**: 2026-09 · Extends ADR 0003
(versioning) and the conflict scenario (merge validated by a human, replanning). The version model, the merge and the
baseline storage are refined by [ADR 0032](0032-branches-as-pointers-baselines-as-deltas.md) (versions joining
branches, no merge copies, baselines as deltas); §2 by ADR 0101 / 0102 (rebase before landing, commit then merge);
the option branches of §3 / §5 by [ADR 0079](0079-drafts-and-versions-at-landing.md) §5 (an option is a flow of
drafts, with no branch of versions); §4's policy values amended by ADR 0067.

## Context
1. Two concurrent changes can modify the same nodes: a conflict must produce a **merge
   operation** pending human validation, which can invalidate change actions and restart
   planning in the post-merge context.
2. A node version must be creatable **in several branches in parallel**, git-style.
3. Analyzing a modification explores **several options**, each in its own branch, **compares** them, then
   **decides**; an impossible decision must produce its **why** and an **additional analysis
   goal**, until the decision can be finalized; the chosen option is **merged** onto the
   main branch.

## Decision

### 1. Versions and branches
Each node version carries:

| Field | Role |
|---|---|
| `version` | increasing integer **per node**, across all branches (identity `NODE@v7`) |
| `branch` | branch name (`main` by default) |
| `parents` | originating version(s): one for `revise` / `derive`, two for a merge |
| `reason` | `create` · `revise` (successor on the same branch) · `derive` (start of a parallel branch) · `merge` · `adopt` (a change branch made equal to an adopted flow, ADR 0025; no longer written since flows keep no branch, ADR 0079) |

```
REQ-1  v1(main) ── v2(main, revise) ─────────────── v6(main, merge ← v2 + v5)
          └────── v3(opt-a, derive) ── v4(opt-a, revise)
          └────── v5(opt-b, derive)
```

- A **branch** is an object `{name, parent, forkBaseline, head, origin (change / option / flow), status: open | merged | abandoned}`.
  Baselines belong to a branch; each has one parent baseline (`parentId`): a branch is a chain of baselines
  from its fork.
- The ADR 0003 rule is unchanged: outgoing links belong to the source version; a
  derived version carries its outgoing links forward within its branch, and incoming links from other branches become suspect.
- "Latest version" is read **per branch** (`latest(node, branch)`). The common ancestor of two versions
  (walking up `parents`) is the base of the 3-way merge.

### 2. Conflicts and merge
- Every change works on its own branch (ADR 0015 §5); merging it takes that branch into its parent
  (`MergeChange`, `ApplyChange` = commit + merge, ADR 0102): a fast-forward when the parent did not move, else a 3-way
  merge per property from the common ancestor (`PlanMerge` / `MergeBranch`). What merges cleanly lands; a change
  impact of another open change whose `pre` is no longer the head is moved to it and flagged to re-check (ADR 0024 §5).
  Since ADR 0101 / 0102 a change is rebased onto the head of its target before it lands, so the 3-way merge is only
  the race of a target moving in between.
- Unresolved conflicts leave the change `committed` (first called `merge_pending`); a human resolves them per node
  and completes the merge (`MergeChange` with the resolutions); the planner replans whatever became obsolete. In a run,
  `graph.apply` then waits on a `merge` task (per conflicting property: base, ours, theirs, proposed), settled by
  `ResolveMerge`.
- *Planned*: earlier detection (open changes and options relying on outdated versions marked while the parent
  branch advances), a merge proposed by an agent.

### 3. Options: one branch per hypothesis
A change can open **options**: `{id, name, hypothesis, branch, status: exploring | evaluated | selected | rejected}`.

- The analysis (change impacts without a written version) stays at the change level and is shared; the
  versions an option writes (the `post` of its change impacts) belong to the option's branch.
- An option is **materialized** on its branch (`derive` from the change's baseline) when an analysis
  needs the resulting graph (propagation, simulation, checks).
- Exploring an option is a **sub-process** (sub-agent) working on the option: it writes on the option's
  branch without touching `main`.
- *Since then*: an option is a flow of the change opened as a hypothesis (ADR 0032 §6), and a flow keeps no branch
  of versions: what an option writes are its own drafts, which no other flow sees (ADR 0079 §5).
- **Comparison** is a `comparison` artifact (criteria × options, scores, rationale) produced by
  an action (LLM, script); available CEL conditions: `options`, `options.all(o, o.status == "evaluated")`…

### 4. Decisions and decision loops
The decision is a **decision point** on the blackboard: `{question, options, criteria, status:
open | blocked | decided, decider, justification}`.

```
            ┌──────────────► explore options ──► compare ──┐
            │                                                     ▼
   open questions ◄── "cannot decide: why" ◄── decide ──► decision finalized
   (analysis goal)                                                              │
            └── analysis sub-agent (identified by the intent = the why)   ▼
                                                                          merge the option onto main
```

- The decider (human, or agent with human ratification depending on the methodology) can answer
  **"undecidable"** with a **why**: this creates `question` items (open) on the blackboard.
- Conditions of the `decisions` library (ADR 0064): `open_questions` / `no_open_questions`. The decision action requires
  `no_open_questions`; a generic `investigate` action has the effect of answering questions: it
  launches a **sub-agent** whose intent is the question — identification picks the suitable analysis
  agent, in this methodology or another (multi-methodology axis).
- The answer (`answer` artifact linked to the question) closes the question; the world changes; the planner
  naturally returns to the decision. No explicit goal stack: the loop emerges from the blackboard.
- Safeguards: maximum number of rounds and budget (tokens, duration) per decision point; beyond that,
  escalation to a human.

### 5. Finalization and merge
Once the decision is made: the chosen option moves to `selected`, its branch is **merged onto `main`**
(flow from §2, with human validation of conflicts), the other options move to `rejected` and their
branches to `abandoned` (kept for audit: what was considered and why it wasn't chosen remains known).
The change then continues toward its application or its release. *Since then* (ADR 0079 §5): selecting an option
adopts its flow (its drafts become the main flow's, a conflict when the main flow's draft moved on since), rejecting
one discards its flow, whose drafts stay in the log for the audit.

## Consequences
- The version model gains `branch`, `parents`, `reason`; `latest` becomes per-branch; "latest version"
  queries and suspect-link detection take the branch as a parameter.
- The blackboard gains options, decision points, questions and answers; the IDE gains an option comparison
  view, a 3-way diff, and a version graph view.
- Sub-agent calls must be able to target another methodology (`runAgent("methodology/agent", …)`).

## Complementary decisions (validated)

1. **Numbering**: increasing integer per node + branch name.
2. **Materialization of options**: lazy (on demand from an analysis).
3. **Decider**: a **methodology agent** makes the decision with a **confidence** level; below the
   threshold declared by the decision point, the decision becomes a pending human action (ratification).
4. **Budget**: set during the analysis phase, **at the change level** (tokens, steps, duration). The engine
   tracks consumption; conditions expose `budget` (`remaining`, `ratio`, `low`, `exhausted`).
   When the budget is low, methodologies switch to **accelerated, suboptimal** paths
   (deciding with the information at hand instead of investigating); once exhausted, decision loops are
   forcibly closed by the decider, and if needed by a human.
5. **Multi-methodology = specialization**: an action can be **abstract** and have
   **specializations** (same role, different rules: `build` in C, in Java, in shell), declared in the
   same methodology or in others (`specializes: "<methodology>/<action>"`), with a CEL **guard**
   (`when`) and a priority. The planner reasons over the abstract action; at execution time, the engine
   picks the highest-priority applicable specialization.
   *Implemented (M9): `specializes` / `when` / `priority` fields, `kind: abstract`, choice at execution time.*
6. **Subtyping** (validated principle, examples remain illustrative): specialization applies to
   **domain object types** as well as to **actions**. A node type can specialize another
   (`extends`, e.g. `alm@SecurityRequirement` ⊂ `alm@Requirement`, `alm@JavaComponent` ⊂ `alm@Component`): it
   inherits its properties and allowed link types, and a condition, an `expects`, or a guard written on the
   parent type applies to subtypes (`"alm@Component" in x.types`). Action specializations naturally select based
   on the subtype of the object being processed (guard `when` on `"alm@JavaComponent" in x.types`).
   *Implemented (M9): `extends` on node types, `x.types` (the type and its supertypes) in conditions.*

## Implementation status

- **Done**: versions per branch (`branch`, `parents`, `reason`), branches, `latest` per branch, 3-way merge of
  branches (`PlanMerge`, `MergeBranch`), change branches merged with `committed` + `MergeChange` and the `merge` task
  of a run (§2), specialization (complementary decision 5), subtyping (complementary decision 6).
- **Done ([ADR 0032](0032-branches-as-pointers-baselines-as-deltas.md) §6, ADR 0079 §5)**: options (§3) as flows
  opened as hypotheses, with an active option the change works on, compared (`DiffFlows`, ADR 0083), evaluated, and
  the finalization (§5): the selected option's flow is adopted (its drafts become the main flow's), the others are
  rejected and their flows discarded.
- **Done (§4, complementary decision 3)**: decision points `{question, options, criteria, decider, threshold,
  maxRounds, deadline}` replayed from facts of the main flow (`KindDecisionPoint`, `pkg/domain/decision.go`), with
  the statuses `open | blocked | ratifying | escalated | decided` (the ADR's `blocked` is kept; `ratifying` and
  `escalated` are the confidence threshold and the safeguards). A ruling is *decided* (option, confidence,
  justification) or *undecidable* (the why, and the questions: open `question`s that block the point); an answer
  closes a question and the planner comes back to the decision. An agent ruling below the threshold waits for a
  person's ratification (refusing it counts as a round); after `maxRounds` rulings that did not settle the point,
  or past its deadline, it is escalated and only a person rules it. A decided point selects its option (§5);
  the merge refuses a change with an open decision point (ADR 0102). The CEL variables `options`, `activeOption`,
  `decisionPoints`, `questions`, and the conditions `open_questions` / `no_open_questions` (with `decision_ready`,
  `decision_pending`, `no_decision_pending`, `ratification_pending`, `decision_escalated`, `options_open`,
  `options_evaluated`, `option_selected`) of the `decisions` library, which a methodology imports (ADR 0064, it was
  first known to every methodology). The generic
  `investigate` action is the builtin `decision.investigate`: one sub-agent per open question, whose intent is the
  question, identified among every methodology (the multi-methodology axis); its outcome answers the question.
  Actions rule through items of kind `decisionPoint`; a human task's ruling is a person's.
  **Amended by ADR 0067**: `threshold`, `maxRounds`, `decider`, the deadline and the rounds are the values of a
  *policy* (`pkg/decision`), kept by the point as opaque `Policy` values and interpreted by `Graph.DecisionPolicy`;
  the graph keeps only the mechanism.
- **Not done**: the planned part of §2; the change budget (complementary decision 4: `budget` in conditions, and the
  token budget of a decision point: its safeguards are the rounds and the deadline); `runAgent` of a sub-agent of
  another methodology from a script (`decision.investigate` identifies across methodologies, `runAgent` stays in
  the parent's).
