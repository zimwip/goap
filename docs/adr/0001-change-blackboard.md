# ADR 0001 — The blackboard is the *change* axis of the graph

**Status**: accepted · **Date**: 2026-09

## Context
Embabel uses an in-memory blackboard containing typed objects. For enterprise
methodologies, an agent's work must be persistent, auditable, shareable between humans and
agents, and linked to the reference repository it modifies.

## Decision
A process's blackboard is a **Change** stored by the graph service. It holds the change's facts
(items of kind `decision`, `artifact`, `merge`, `flow`) and its **change impacts** (ADR 0024): the
nodes it declares, each with the exact version it starts from (`pre`) and the version it writes
(`post`). Actions write facts and versions on the change's own branch (ADR 0015 §5); the target
branch is modified only when the change is applied (`ApplyChange`, a merge that produces a new
baseline).

## Consequences
- Full traceability (`producedBy` / `derivedFrom` provenance) and possible resumption of a process.
- Several processes / humans can contribute to the same change.
- The engine must re-read (hydrate) the blackboard on every cycle: an accepted network cost, a cycle
  corresponding to one LLM call or one human action.
