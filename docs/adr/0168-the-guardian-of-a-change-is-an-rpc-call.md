# ADR 0168 — The guardian of a change is a call to the methodology registry

**Status**: accepted, implemented (Go and Rust) · **Date**: 2026-10 · Supersedes the ADR 0163 addenda "what a change
follows" and "the conditions, the gates and the guardians" (the graph instantiating the methodologies), amends ADR 0066
(the hooks instantiated by the graph from a read-only registry), ADR 0103 (the roles of the operations), ADR 0109 §4
(the step dependencies of the history).

## Context

The graph runs the lifecycle of a change and judges its commits, but what decides is the methodology of the change:
the version it follows, its main goal, the lifecycle it names with the criteria and freezes of its gates, the guard of
each move, the guardian of its commits and the chain of guardians integrating it, the cascade of the activities of
its sub-changes, the roles allowed each operation, the steps its history weighs. Until now the graph compiled the
methodologies itself to answer: Go's `platform/cmd/graph` held a read-only `registrysvc.Service` over the platform
graph, and the Rust graph re-implemented the reading of the stored definition, the derivation of the conditions, their
activation and their evaluation (`graph_core::methodologies`, `conditions`, `graph_script::conditions`), checked
against Go by golden cases.

That made the graph know the methodology, which it should not: the domain is the responsibility of the registry and
the graph (ADR 0167), the methodology is the methodology registry's. Each graph compiled every methodology again, a
second compiler (in Rust, a third) had to keep up with every change of the language, and the compiled forms followed
the methodology namespace only by reading it again.

## Decision

1. **The graph knows no methodology; it offers hooks.** Go's `graph.ChangeLifecycles` (`MethodologyVersion`,
   `Lifecycle`, `DefaultGoal`, `Guard`, `Gate`, `Commit`, `Guardians`, `Integrates`, `CheckGuardians`),
   `SubChangeValidator`, `OperationAuthorizer`'s roles and `LogFacets`' step dependencies are what the graph needs of
   a methodology; Rust has the same as one trait, `graph_core::guardian::Guardian`, carried by the session
   (`ReadTx::guardian`, `Session::with_guardian`, `State::guardian`). With none plugged a change follows its
   methodology by name alone: no lifecycle, its commits on the committable states, its operations the
   administrators'.
2. **The composition plugs them as calls to the methodology registry**, a new service of its contract,
   `registry.v1.ChangeGuardianService` (`proto/goap/registry/v1/guardian.proto`), served by `platform/cmd/registry`
   (`registrysvc.GuardianHandler` over the `Service` that compiles them, with its cache). Go's client is
   `lib/guardianclient` (`platform/cmd/graph`), Rust's `graph_service::guardian::Client` (`goap-graph-rs`); both reach
   the registry at `GOAP_REGISTRY_URL`. The graph sends what it judges on: the blackboard of the change, read in the
   transaction of the move or the commit (ADR 0140), as `GetBlackboard` answers it, with the facets it adds (the
   dependencies, the criticality policy of the organisation, the history), each the JSON of its value
   (`registry.v1.Blackboard`, `pbconv.BlackboardToPB` / `FromPB`, `domain.RegisterFacet`). A lifecycle, a transition
   and a gate's result travel as their JSON.
3. **The registry compiles once and follows the commits.** Its store keeps the heads of the methodology namespace
   (`GraphStore.Follow`, `FollowEvents`) until a baseline event of that namespace on main, or its own commit, moves
   them: a guard asked inside a graph's write transaction is answered from the compiled cache, never by reading the
   graph back (a read the graph would serve after the transaction that waits for the answer).
4. **What the graph decides alone it does not ask.** A change scoped to no Activity is judged on its committable
   states, a change guarded by its own methodology alone asks no guardian chain, a sub-change of no activity passes
   the cascade, a change of no methodology is restricted by none: the clients answer these without a call, so the
   platform bootstrap, which runs before the registry, needs none.
5. **The graph's own instantiation goes.** Go: `platform/cmd/graph` no longer builds a `registrysvc.Service`. Rust:
   `graph_core::methodologies`, `conditions` (stored, derive, activation, board), `graph_script::conditions`, the
   methodology part of the platform snapshot of a content graph, and their golden cases
   (`TestRustActivationCasesAreCurrent`, `TestRustLibrariesAreCurrent`, `TestRustMethodologyCasesAreCurrent`) are
   removed. The history fold stays the graph's (`history::fold`, its golden cases): it is the graph's facet, only the
   step dependencies are asked.

## Consequences

- One compiler of the methodologies, the registry's; a graph's answer on a guard is the registry's, in Go as in Rust.
- A move of the state of a change, a commit of a change scoped to an Activity or guarded by another methodology, and a
  creation following a methodology now need the registry up: an unreachable registry refuses them (unavailable),
  where it used to be read in the graph. The registry depends on the graph for its store; the graph depends on the
  registry only for the changes that follow a methodology.
- Each call carries a whole blackboard; a methodology with many impacts sends more than a call reading its nodes
  would. The registry may later read the graph by view instead, which ADR 0157 allows.
