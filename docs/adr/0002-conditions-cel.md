# ADR 0002 — Conditions expressed in CEL

**Status**: accepted · **Date**: 2026-09

## Context
GOAP preconditions / effects are named booleans. They must be computed from the state
of the change, which references domain elements. The language must be declarative (YAML),
safe (executed server-side), fast, and verifiable at load time.

## Options
1. Registered Go code (flexible, but neither declarative nor deployable without a build).
2. JSONPath / JMESPath (no typed quantifiers, unclear error messages).
3. **CEL** (typed, not Turing-complete, `all`/`exists`/`filter` macros, cost bound, used by
   Kubernetes/Envoy).
4. Rego (powerful but policy-oriented, heavier).

## Decision
CEL (`cel.dev/cel-go`). Variables: `change`, `items`, `impacts`, `proposals`, `decisions`,
`artifacts`, `vars`. Domain references are hydrated (`type`, `key`, `props`, `out`, `in`,
`latest`). An expression that errors makes the condition **unknown**: it satisfies no
precondition. Actions' `expects` are compiled into CEL.

## Consequences
- Methodologies are validated at publish time (compilation + bool type check).
- No network call during evaluation; navigation depth is limited to hydration
  (direct neighborhood). Traversal functions can be added later if needed.
