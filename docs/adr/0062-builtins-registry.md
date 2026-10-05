# ADR 0062 — Builtin actions are checked against a registry; the self-improvement builtins live outside the engine

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0011, 0028, 0034, 0061.

## Context

An action of kind `builtin` names a Go function. Nothing checked the name before run time (`unknown builtin` when the
action was scheduled), so a typo in a methodology was published and failed late. The names were string literals
spread over the engine, the compiler and the YAML. The engine package also implemented the self-observation
builtins (`observe.analyze`, `observe.propose`, `methodology.draft`) and so imported `pkg/observe`, which hardcodes
`methodology@...` node types: the engine was coupled to one methodology.

## Decision

1. `pkg/builtins` is a leaf package (no project import) holding the builtin names (`GraphPropagate`, `GraphApply`,
   `DecisionInvestigate`, `ProcessStep`, `ObserveAnalyze`, `ObservePropose`, `MethodologyDraft`), `All()` and `Known`,
   the static set of those names.
2. `methodology.BuiltinSet` (`HasBuiltin(name)`) and `Methodology.Builtins`, set by `WithBuiltins` like `Types` by
   `Resolve`: the compiler reports `unknown builtin` for an action of kind builtin when a set is given, and always
   accepts `process.step`. Nil: not checked (tests, loaders). `lintTypeRefs` still reads the `linkTypes` param of
   `graph.propagate` only; a params schema per builtin is a later step.
3. The registry resolves every methodology with `builtins.Known{}` (`registrysvc.resolve`), so a wrong name is
   refused when it is validated, saved as an issue and refused at publish, without an engine.
4. `engine.BuiltinExecutor` is the engine's table: `Register(name, f)` panics on a duplicate, `HasBuiltin`
   implements `BuiltinSet`. `DefaultBuiltins()` registers the four generic builtins.
5. The self-observation builtins moved to `pkg/selfimprove` (`Register(b, e, Config)`, with `TraceSource`,
   `MethodologyDrafts`, `Config`), called by `cmd/engine` and `cmd/goap-dev` after the engine is built. `pkg/engine`
   no longer imports `pkg/observe` (`pkg/layering` tests it).

## Consequences

- A new builtin is a constant in `pkg/builtins` plus a `Register` call; a name missing from the list is refused by the
  registry even if an engine would register it.
- A deployment that registers extra builtins in the engine only still runs them, but the registry refuses the
  methodologies naming them until they are added to `pkg/builtins`.
