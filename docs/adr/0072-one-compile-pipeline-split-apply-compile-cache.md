# ADR 0072 — One compile pipeline, a split apply check, a compile cache

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0023, 0034, 0051, 0056, 0058, 0062.

## Context

`Methodology.compileWith` was one 370-line function; `Compile`, `CompileLenient`, `Validate`, `ValidateStored`, the
registry's own `lenient()` wrapper (which turned a compiler panic into a user-facing issue, hiding bugs) and `PreviewPlan`
(strict, so a draft that `GetProcessGraph` drew was refused a preview) were five policies spelled five ways. The registry
compiled a methodology on every `Methodology` / `List` call, which the engine makes per event (companions) and per action
(specializations). `applier.checkChangeImpacts` mixed the property checks, the transition walk and the gate decision.

## Decision

- **Compile pipeline** (`pkg/methodology/compile.go`): `compileWith` orchestrates one `compileState` method per phase
  (`checkHeader`, `compileConditions`, `injectLibraries`, `compileActions`, `checkSpecializations`, `checkReferences`,
  `compileGoals`, `compileAgents`, `checkTransverse`, `mergeRolesMethodsProcesses`, `finish`); the order is a constraint
  (the `expect:*` conditions are known after the actions, the agents are complete before methods and processes merge) and
  the issues are sorted at the end. The methodology is never modified: generated effects live on copies of the actions.
- **One policy type**: `CompileWith(CompileOptions{Lenient, Stored})`. `Compile`, `CompileLenient`, `Validate`,
  `ValidateStored` are one-line policies of it; `Stored` adds the "namespace required" rule of a stored methodology
  (unchanged: which methodologies are accepted does not change).
- **A compiler panic is a bug**: the registry no longer recovers it into an issue. It surfaces to the RPC server's
  recovery like any other bug.
- **`PreviewPlan` is lenient** like `GetProcessGraph`: it plans over what stands of a draft and returns the issues of the
  methodology in the response's `issues` (no proto change); an unknown agent or goal of a draft with issues is reported
  with the issues rather than as not-found.
- **Apply check**: `checkChangeImpacts` is `walkChangeImpacts` (property validation, editable-state collection, per node
  `walkTransitions` with the collect / authorize switch) then `settleChangeImpacts` (collect pass stops, landing gate,
  editable refusal, `checkTransition`, `runActions`). The two-pass design of `authorizeMoves` is unchanged.
- **Compile cache** (`internal/registrysvc/compilecache.go`): the key is (name, version, hash of the definition's JSON,
  the published domains in force as `name@version`), so a stale hit cannot happen (a draft replaced in place, a domain
  republished, are other keys); the value is the compiled methodology or its error. It is bounded (256 entries, then it
  starts over), guarded by a mutex, and emptied by every write the service publishes (`publish`, `publishDomainEvent`:
  save, publish, delete, domain writes). The engine stays unaware (it sees the same `MethodologyPort`).

## Consequences

- A compiled methodology is shared between callers: it is read-only (as it already was through `Client`'s cache).
- The registry `Client` (other process) keeps its own per-name-and-version cache; it is not covered.
- A multi-process registry sharing one database is safe: the content hash and domain versions are part of the key.
