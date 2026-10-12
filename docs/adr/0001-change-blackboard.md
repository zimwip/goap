# ADR 0001 — The blackboard is the *change* axis of the graph

**Status**: accepted · **Date**: 2026-09

## Context
Embabel uses an in-memory blackboard containing typed objects. For enterprise
methodologies, an agent's work must be persistent, auditable, shareable between humans and
agents, and linked to the reference repository it modifies.

## Decision
A process's blackboard is a **Change** stored by the graph service. It holds the change's facts
(items of kind `decision`, `artifact`, `merge`, `flow`, ...) and its **change impacts** (ADR 0024): the
nodes it declares, each with the exact version it starts from (`pre`) and what it writes (`post`: a draft until
the change lands, ADR 0079). Actions write facts and drafts on the change; its versions are written on the change's
own branch when it is committed (`CommitChange`, ADR 0102), and the target branch is modified only when the change is
merged (`MergeChange`; `ApplyChange` commits then merges), which produces a new baseline.

## Consequences
- Full traceability (`producedBy` / `derivedFrom` provenance) and possible resumption of a process.
- Several processes / humans can contribute to the same change.
- The engine must re-read (hydrate) the blackboard on every cycle: an accepted network cost, a cycle
  corresponding to one LLM call or one human action.
