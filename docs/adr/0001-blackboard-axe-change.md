# ADR 0001 — The blackboard is the *change* axis of the graph

**Status**: accepted · **Date**: 2026-09

## Context
Embabel uses an in-memory blackboard containing typed objects. For enterprise
methodologies, an agent's work must be persistent, auditable, shareable between humans and
agents, and linked to the reference repository it modifies.

## Decision
A process's blackboard is a **ChangeSet** stored by the graph service. Its elements
(`impact`, `proposal`, `decision`, `artifact`) reference **exact versions** of nodes in the
reference graph. Actions only write ChangeItems; the domain graph is modified
only by `ApplyChange`, which produces a new baseline.

## Consequences
- Full traceability (`producedBy` / `derivedFrom` provenance) and possible resumption of a process.
- Several processes / humans can contribute to the same change.
- The engine must re-read (hydrate) the blackboard on every cycle: an accepted network cost, a cycle
  corresponding to one LLM call or one human action.
