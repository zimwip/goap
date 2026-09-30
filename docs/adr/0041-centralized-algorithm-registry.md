# ADR 0041 — A platform-wide algorithm registry, at the registry-DB tier

**Status**: accepted, implemented · **Date**: 2026-09 · Extends ADR 0018 (algorithms), ADR 0019 (adapters),
ADR 0023 (domains in the registry's database).

## Context

ADR 0018 declares algorithms entirely inside a domain's own YAML (`Schema.algorithms`,
`Schema.algorithmInstances`), stored as nested JSON in that domain's own row of `domain_version`
(`internal/registrysvc/sqlstore.go`). Each domain silos its own copy: a second domain that wants the same
regex validator, or the same "stamp the approval date" transition action, must redeclare it verbatim. ADR
0018's own "Not done" list names this gap: "algorithms shared across domains".

The obvious analogy is `AdapterDef`/`Adapter` (ADR 0019): a definition in the `platform` namespace of the
**graph**, instantiated with parameter values by organisational units, and the methodology-element pattern
(ADR 0023): a version authored in YAML but materialized as real graph nodes (`methodology@Agent`, ...) for
a single source of truth. Both were considered for algorithms and rejected.

## Decision

1. **Centralize at the registry-DB tier, not the graph.** `NodeType.validators` and
   `LifecycleTransition.guards`/`actions` are resolved **structurally**, at type-catalogue build time
   (`pkg/typecat`), and the type catalogue is loaded from the registry's database *before any graph
   exists* — domains are kept out of graph data for exactly this reason (they define the graph, they are
   not graph data). Materializing `Algorithm` as a graph node (`platform@Algorithm`) would make the type
   catalogue depend on a graph that itself depends on the type catalogue: a bootstrap circularity.
   `AdapterDef` avoids this because an adapter is resolved lazily at MCP-call time
   (`internal/mcpsvc/service.go`'s `adapterAlgorithm`), never needed to bootstrap the graph — algorithm
   validators/guards/actions are needed at bootstrap, so they cannot follow that pattern.
2. **A domain still declares its algorithms directly.** The YAML author experience is unchanged
   (`algorithms:`, `algorithmInstances:`); nothing new to learn for the common case of a domain that owns
   its algorithms outright.
3. **Publishing a domain centralizes its algorithms.** `Service.PublishDomain` walks the domain's
   `Algorithms` and upserts each into a new, name-keyed table, `algorithm_registry`, at the same tier as
   `domain_version` (`internal/registrysvc/migrations*/0002_algorithm_registry.sql`,
   `AlgorithmStore`/`SQLAlgorithmStore`/`memoryAlgorithms`, mirroring `DomainStore`/`SQLDomainStore`
   exactly). A name not yet centralized is inserted; a name already centralized with an **identical**
   definition (type, language, code, params) is a no-op (two domains independently declaring the same
   reusable algorithm verbatim); a name already centralized with a **different** definition refuses the
   publish, naming the owning domain.
4. **A domain references a centralized algorithm as `platform@<name>`.** `AlgorithmInstance.algorithm`
   reuses the `<namespace>@<name>` textual convention of type references (ADR 0012) for this new purpose:
   a bare name resolves in the domain's own `Algorithms` exactly as before (`algo.Set.Bind`); a
   `platform@<name>` reference resolves against the platform-wide registry instead
   (`algo.PlatformRef`, `algo.Set.Platform`), and the domain declares **no** local `algorithms:` entry of
   that name. This is a reference to the registry-DB tier's shared table, not to a graph node — despite
   the textual overlap with `platform@AdapterDef`, it never touches the graph.
5. **Existence and type are checked where the registry is reachable.** `pkg/methodology`'s own
   `checkAlgorithms` validates shape only (a `platform@<name>` reference is syntactically accepted without
   a local match, since the package has no registry access). `BoundValidators` / `OwnBoundValidators` /
   `BindLifecycle` already silently drop an instance that fails to resolve rather than fail the catalogue
   build (ADR 0018 §5); left unchecked, an unresolved `platform@` reference would fail **silently** at run
   time. `internal/registrysvc.Service.checkPlatformAlgorithms` closes this: on `SaveDomain` /
   `PublishDomain`, once the registry's platform algorithms are loaded, it re-binds every validator and
   every guard/action and reports a count mismatch as a validation issue.
6. **Wiring**: `typecat.NewWithPlatform(platform algo.Set, ds ...*methodology.Domain)` is `typecat.New`
   with `platform` threaded through `resolve` into `Schema.OwnBoundValidators`/`BindLifecycle`;
   `typecat.NewLiveWithAlgorithms(src Source, algSrc AlgorithmSource)` reloads both together on the same
   registry events (a domain publish is currently the only writer of the algorithm registry, so no
   separate "algorithm published" event is needed). `RegistryService.ListPlatformAlgorithms` (Connect RPC)
   and `registrysvc.Client.Algorithms` serve it to the `engine` and `graph` services, which run in separate
   processes from `registry`.

## Consequences

- A domain author still writes exactly the YAML ADR 0018 describes; centralization is invisible until a
  second domain wants to reuse a name, at which point it drops the local `algorithms:` entry and writes
  `platform@<name>` in its instance.
- `algorithm_registry` has no draft/published lifecycle of its own (unlike `domain_version`): the store is
  a dumb key-value (`SaveAlgorithm` upserts), and `Service.centralizeAlgorithms` carries the
  identical-or-conflicting business rule. A registry deployed without an `AlgorithmStore` degrades
  gracefully: centralization is skipped (not required) and no `platform@` reference resolves, matching
  every pre-existing `Service` wiring that predates this ADR.
- Not done: renaming or deleting a centralized algorithm once another domain references it; a
  `GetDomainUsage`-style "which domains reference `platform@X`" query; a dry run of a redeclaration
  conflict before publish (today it is only reported at `PublishDomain`, though `SaveDomain` already
  reports an unresolved `platform@` reference through `checkPlatformAlgorithms`).
