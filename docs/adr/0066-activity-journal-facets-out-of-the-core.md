# ADR 0066 — Activity, execution journal and blackboard facets out of the graph core

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0011, 0030, 0059, 0065. §1's landing gate
superseded by ADR 0102 (the guardian's say on a merge, `ChangeLifecycles.Merge`).

## Context

After ADR 0065 the graph core still knew three use cases: the Activity of a change (`Change.ActivityRef`, a column,
`Graph.ActivityGoalsMet`, a walk over `methodology@sub_activity` links in the methodology namespace), the agent
execution journal (`domain.ExecutionRecord`, `Graph.Record` / `Journal`, the prompt cap) and the options and decision
points as fixed fields of the blackboard.

## Decision

1. **Activity.** `ActivityRef` is no longer a column, a field of `Change` or of `NewChange`: it is
   `Change.Data[registrysvc.DataActivity]` (`registrysvc.ActivityOf(c)`). The graph asks two hooks, both wired in
   `goap-dev` from the registry service (`platform/cmd/graph` holds no registry: it wires neither, as for `Lifecycles`):
   - A landing gate, `(decided, ok bool, err error)`, asked outside the transaction (it may read the graph): `decided`
     false, the floor of the node-type lifecycles applies; true, `ok` replaces it. First `Graph.LandingGate`, asked at
     Apply / Commit; since ADR 0102 it is the guardian's say on a merge, `ChangeLifecycles.Merge`
     (`registrysvc.Service.Merge`, the Activity's goals), asked by `checkMerge`, and `LandingGate` is gone.
   - `Graph.SubChangeValidator func(ctx, parent, child) error`, asked by `CreateChange` before its transaction when it
     has a parent. The registry implements the cascade (the child's activity within the parent's through the
     `sub_activity` links, read from the methodology namespace) and wraps `graph.ErrInvalid`.
   `graph/graph` names no `methodology@` type, `NamespaceMethodology`, `sub_activity` or `ActivityRef`
   (`platform/layering`'s `TestGraphNamesNoMethodology`).
2. **Execution journal = `contracts/journal`.** `Record` (formerly `domain.ExecutionRecord`), `ModelCall`, `ModelExchange`,
   `ToolUse`, `Filter`, the kinds (`KindTick`...) and the streams (`StreamJournal`, `StreamModel`). The graph keeps a
   generic typed log: `Graph.AppendLog([]domain.LogEntry)` (refuses the `fact` and `impact` streams, which only the
   graph writes, and an unknown change) and `Graph.ChangeLog` (a change or processes is required).
   `journal.Entries(r)` turns a record into its entries (the record, then one `model.call` per exchange, capped at
   `MaxExchangeText`, stripped from the record), `journal.Append(ctx, log, recs)` / `journal.Read(ctx, log, filter)`
   work over a `journal.Log` (`*graph.Graph`, `graphsvc.Client`), `journal.Decode` / `DecodeExchange` read entries
   back. `engine.GraphPort` has `AppendLog` / `ChangeLog` instead of `Record` / `Journal`. The wire carries log entries for
   the write: `RecordExecutions` and the `ModelExchange` messages are replaced by `AppendLog` (and the existing
   `ListChangeLog`); `ListExecutions` stays as the read of records (the web uses it), the handler decoding the
   entries. `journal.Record.ActivityRef` stays on the journal record (it is a journal field, not the core's).
   `graph/prov`, `engine/observe`, `engine/engine`, `engine/selfimprove`, `graph/graphsvc` import `contracts/journal`; `graph/graph`
   and `contracts/domain` must not, and `contracts/journal` must not import `graph/graph` or the engine (`platform/layering`).
3. **Blackboard facets.** `Blackboard` keeps the change, its nodes, neighbours, vars, supertypes and `At`; the options,
   the active option and the decision points are facets, `Blackboard.Facets map[string]any`
   (`domain.FacetOptions`, `FacetActiveOption`, `FacetDecisionPoints`), read through `domain.OptionsOf`,
   `ActiveOptionOf`, `DecisionPointsOf`, `domain.Facet[T]` and set with `Blackboard.WithFacet` (copy). The graph fills
   the built-in ones (options and decision points remain graph mechanisms) and the providers of `Graph.Facets`
   (`BlackboardFacet func(c, now) any`, called in the reading transaction, so without I/O). The wire shape of
   `GetBlackboard` is unchanged. `checkGate`'s `consumed` handling is unchanged (it replaces the decision-points facet).

## Consequences

- A use case adds what a change carries (Data), what lands it (the guardian's `Merge`), what a sub-change may be
  (SubChangeValidator), what the blackboard shows (Facets) or what the log holds (a stream) without touching the core.
- Typed access to the three built-in facets goes through accessors (a type assertion each); the `Facets` map is not
  serialised (`json:"-"`).
- PostgreSQL: `0001_schema.sql` edited in place (no `activity_ref`), `postgres.go` edited; not executed here.
