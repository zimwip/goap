# ADR 0060 — A methodology is a domain; the domain definition model leaves `engine/methodology`

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0012, 0013, 0023, 0055.

## Context

`engine/methodology` held two models: the methodology (conditions, actions, agents, processes, compilation) and the
definition of a domain (`Domain`, `Schema`, node and link types, attributes, enums, lifecycles, algorithms, the
built-in domains). The type catalogue (`lib/typecat`), the graph and the registry imported it for the domain half only,
which inverted a rule of the design: a domain knows no methodology. A methodology is itself described by a domain, the
built-in meta-domain `methodology` (ADR 0023); the Go types of its elements stay static and evolve with
`lib/domains/builtin/methodology.yaml`.

## Decision

1. The domain definition model lives in `lib/def` (package `def`): `Domain`, `ParseDomain`, `Validate`,
   `LoadDomains`, `Schema` and its checks, `NodeType`, `LinkType`, `Attribute`, `Enum`, `StructureTag`,
   `SearchProperty`, the algorithm binding (`BoundValidators`, `BindLifecycle`), `BuiltinDomains` /
   `IsBuiltinDomain` (the embedded `lib/domains/builtin`), `DomainTypes`, `TypeSet`, and what both models share: `Issue`,
   `Issues`, `NameRE`.
2. It cannot live in `contracts/domain` itself: it uses `lib/dsl` to check algorithm code, and `lib/dsl` imports `contracts/domain`.
   A sub-package keeps the family together without the cycle.
3. `engine/methodology` keeps only what is specific to a methodology and imports `def` for the shared pieces
   (`Methodology.Types` is a `def.TypeSet`, validation returns `def.Issues`). `Resolve`, the type-reference lint,
   `Namespaces` and `LoadFile` stay there: they are the methodology's use of domains.
4. No alias is left behind: every importer (`lib/typecat`, `graph/graph`, `engine/engine`, `platform/registrysvc`,
   `mcp/connectors`, the commands) names `def` directly. `lib/typecat` no longer imports `engine/methodology`, and
   the graph uses `def.Attr*` instead of its own copy of the attribute types.

## Consequences

- The dependency runs one way: methodology to domain. A new domain-side feature touches `lib/def` only.
- The domain tests (schema, attributes, lifecycles, subtyping of types, shipped domains) moved with the model.
