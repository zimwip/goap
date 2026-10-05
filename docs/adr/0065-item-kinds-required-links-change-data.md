# ADR 0065 — Item kinds registry, required links on node types, use-case marks in Change.Data

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0036, 0037, 0039, 0040, 0054.

## Context

The graph core and `pkg/domain` still named use cases: the risk register (`KindRisk`, `KindAction`, `KindWaiver` in a
closed `ChangeItem.Validate` switch, `Change.Risks()`), personal units (`domain.PersonalUnit`, `Change.Personal()`), the
`Administrative` flag of a change (a column, a proto field, inherited by sub-changes, read by nobody), the `User`
membership rule coded in `checkRequiredParent`, and a `Graph.PurgePolicy` hook nothing set.

## Decision

1. **Item kinds are registered.** `domain.RegisterItemKind(kind, validate)` adds a kind to `ChangeItem.Validate`; an
   unregistered kind is still refused. Registration is explicit (no `init`), idempotent and mutex-guarded. The
   built-in generic kinds (artifact, decision, flow, signal, transition, ...) stay in the core switch.
2. **The risk register is `pkg/risk`**: `Risk`, `ActionItem`, the waiver, their validation, `risk.Register()` and the
   free functions `risk.Risks(c)`, `risk.Actions(c)`, `risk.Waivers(c, process)` replacing the `Change` methods.
   `cmd/graph`, `cmd/engine` and `cmd/goap-dev` call `Register`, and so does the `TestMain` of the test packages that
   write such items. `pkg/condition` and `pkg/brief` import it (the `risks` library depends on the risk model);
   `pkg/graph` and `pkg/domain` must not (`pkg/layering`).
3. **Personal units live in `pkg/access`** next to `UserKey`, which shares one prefix constant (`access.UserPrefix`)
   with `PersonalUnit` (the user node is its own personal unit, ADR 0037, 0039): `IsPersonalUnit`, `PersonalSubject`,
   `IsPersonal(change)`, `IsPersonalTo(change, subject)`, `PersonalSubjectOf(change)`. `internal/eventsvc` imports
   `pkg/access` for them (no layering conflict).
4. **`Change.Administrative` is gone.** The column, struct field, `NewChange` / `CommitInput` fields and the two proto
   fields are removed; the engine sets `Change.Data[domain.DataAdministrative] = true` from
   `Methodology.Administrative`. The graph reads no key of `Data`; a sub-change does not inherit it (nothing read it,
   and inheriting all of `Data` would also pass on `trigger`). The project selector exemption stays a UX rule of the web.
5. **Required links are declared**: a node type carries `requires: [{link, count}]` (`def.NodeType.Requires`,
   `domain.RequiredLink`, "exactly `count` outgoing links of the type", default 1, inherited through `extends` and
   redefined by link; registry proto `NodeType.requires`). `organisation@User` declares one `member_of`. The
   structure part (parent of an `OrgUnit` / `ProjectUnit`) is unchanged. The checks keep their timing
   (`checkRequiredParent` at `Commit`, `checkParentInvariant` after the write): not a `NodeValidator`. An untyped graph
   uses the catalogue of the built-in domains (ADR 0069).
6. **`Graph.PurgePolicy` is removed**: no production code set it. A rule keeping discarded changes would be added as
   a hook when a use case asks for it.

## Consequences

- Schema and proto changed in place (greenfield): databases are recreated, `buf generate` was run (`make generate`
  fails on a pre-existing lint error in `events.proto`).
- A service that writes or folds risks must call `risk.Register()`; forgetting it makes `AddItems` refuse the kind.
