# ADR 0049 — Mandatory Change for every node, version and link write

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0024 (change impacts), ADR 0029/0030 (impact log), ADR 0040 (user bootstrap, required parent).

## Context

"Every modification is a Change" (this project's second design rule) was enforced only at the RPC gateway
(`internal/graphsvc.refuseDirectWrite`), and only for lifecycle-controlled or access-listed types.
`pkg/graph` core — `CreateNode`, `UpdateNode`, `Link` — stayed fully open to direct writes bypassing Change
entirely, used by seeding (`SeedDemo`, `LinkOrphanUnits`) and dominant in the test suite (~60 call sites). This
closes that gap in the Graph Service itself, not just the gateway: no node, node version or link is written
outside the frame of a change, hardwired, with no caller-level exemption.

## Decision

### `CreateNode` / `UpdateNode` become `Commit` sugar, signatures unchanged

Rejected a required `ChangeID` parameter (would force every caller — ~60 of them — to hand-roll `Commit`'s own
bookkeeping). Instead both now open, populate and apply a single-edit change internally
(`Graph.commitEdits`, the unexported core `Commit` itself calls). `Commit` gained an implicit-baseline
bootstrap: a namespace touched for the first time gets an empty baseline exactly the way
`graphsvc.applyOn` used to do it by hand (now dead code, removed).

**Consequence, verified against the full test suite**: a node created this way now gets a real `ChangeID`, goes
through the impact log (ADR 0029/0030) and any `NodeValidator` (ADR 0048), and — for a lifecycle-typed node —
a real initial state instead of the empty string the old raw write silently left it at. That last point is a
genuine correctness fix with a real consequence: `SeedDemo`'s `alm@Requirement` nodes now start `proposed`
(not editable) instead of `""` (which accidentally bypassed the editable-state gate entirely). Any later edit
of such a node — including the `specify_requirements` LLM step of `methodologies/sdlc.yaml`, and
`internal/connectors/builtin`'s `goap-change/edit` integration tests — must now follow the reopen → edit →
resubmit sequence the system prompt (`pkg/engine/executors.go`'s `llmSystem`) already documents for any
compliant agent. This was already the documented contract for a real LLM; only the tests' hand-scripted mocks
and fixtures needed updating to actually follow it, since they'd been quietly riding the old empty-state bug.

### `checkRequiredParent` / `checkParentInvariant` stay `Commit`-specific, not universal

A real implementation conflict surfaced here, not anticipated at design time: ADR 0040's rule ("a created
OrgUnit/ProjectUnit/User must declare its one required parent/membership link") was, and remains, enforced
only by `Commit`'s own two checks — never by a lower-level write. `SeedDemo` deliberately creates `ORG-ACME`
with no parent (`LinkOrphanUnits` parents it afterward, once `ORG-DEFAULT` exists — exercised by
`internal/graphsvc/seed_test.go`'s `TestLinkOrphanUnits`, which runs `SeedDemo` *before* `SeedDefaults` on
purpose). Routing `CreateNode` through the same `Commit` path as `Commit` itself would have dragged this
unrelated rule along for the ride, breaking that deliberate, tested behavior and forcing dozens of unrelated
test fixtures (`pkg/engine`'s shared `setup()` and friends) to grow parent links they never needed.

Resolution: `Commit` is now `commitEdits(ctx, in, parenting=true)`; `CreateNode`/`UpdateNode` call
`commitEdits(ctx, in, parenting=false)`. Both go through the identical change/impact/Apply/NodeValidator
pipeline; only `Commit`'s own two producer-level guarantees are opt-in, exactly matching their pre-ADR-0049
scope (a raw write never made them either). This is not a loophole against "every write is a change" — it
leaves that invariant intact — it simply keeps a separate, pre-existing business rule exactly as narrow as it
already was.

### `Link` gains a required `domain.ChangeID` — the one real signature break

`Link` is the one kind of mutation that deliberately stays outside `Commit`/`Apply`: it never versions either
endpoint (versioning the source would turn its own outgoing links into suspect links,
`pkg/graph.SuspectLinks` — independently wrong, not just inconvenient, the reason `LinkOrphanUnits` exists at
all). Silently picking an implicit change for it would defeat the point of asking "which change is this link
part of" at all, so the break is deliberate and visible: every caller must now say which open change a link
belongs to. `internal/graphsvc/handler.go`'s `CreateLink` RPC gained a `change_id` field
(`proto/goap/graph/v1/graph.proto`, regenerated via `make generate`) — the one user-facing API change in this
ADR.

### Fallout fixed

- `SeedDemo` (`internal/graphsvc/seed.go`): all its `g.Link` calls now attribute to one change opened on the
  namespace's current head, closed `abandoned` once done (no change impact ever existed for it to land).
- `LinkOrphanUnits` (`internal/graphsvc/seed_defaults.go`): same pattern — one change per repair pass.
- `pkg/graph`'s own test suite needed two new unexported test helpers (`repos_test.go`):
  - `seedNode` — the literal old raw `CreateNode` body, for fixtures that need to arrange a world already
    sitting in an arbitrary (including editable, including non-adjacent) lifecycle state without walking every
    transition to get there. No production code may do this; test fixtures that construct exotic starting
    conditions legitimately need to.
  - `testChange` — opens a change on a namespace's *current* head (creating an empty one only if none
    exists). Needed because `CreateBaseline` unconditionally advances the branch head, even to an empty
    baseline — a naive "just create a throwaway baseline to get a ChangeID for `Link`" pattern silently wiped
    out real content already on main in several fixtures (`pkg/graph/browse_test.go`'s `TestBrowseBaseline`
    actually failed on this; several others were only saved because a correctly-populated baseline happened to
    be created again immediately after, masking the corruption window). `testChange` is now the only pattern
    used for this across `pkg/graph`'s test suite.

## Consequences

- No test file anywhere constructs a node or link outside a change any more; the distinction between
  "change-shaped" (always, now) and "carries `Commit`'s producer-level parenting guarantees" (opt-in, as
  before) is explicit in the code (`commitEdits`'s `parenting` parameter), not implicit in which entry point
  happened to be called.
- `CreateBaseline`'s "always advances the branch, even to empty" behavior is pre-existing and unchanged by
  this ADR, but is now a sharper footgun given how often a throwaway baseline might be reached for
  change-attribution purposes; `testChange` is the safe pattern for `pkg/graph`'s own tests, and any future
  caller should prefer reading the current head over blindly creating a new baseline.
- `methodologies/sdlc.yaml` itself needed no change: its prompts already rely on the shared system prompt's
  lifecycle instructions. Only the test mocks simulating what a real LLM would do needed to actually follow
  them.
