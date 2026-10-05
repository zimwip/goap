# ADR 0048 — NodeValidator plugins, the user lifecycle, and the administrator floor

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0018 (algorithms), ADR 0024 (change impacts), ADR 0029/0030 (impact log), ADR 0043/0046/0047
(roles, platform roles, administration as a platform role).

## Context

Nothing stopped a change from leaving the graph with zero administrators. ADR 0020's floor policy
(`hasRole(admin) → "*","*"`) relies on at least one administrator existing at all times — if the last one is
ever deactivated or loses their grant, the deployment locks itself out with no path back in, short of
operating on the database directly.

Checking this needs two things neither existed:

1. **A way to express "is this account usable" as a graph fact.** `User` had no lifecycle at all — "active"
   and "administrator" were not things a commit could reason about.
2. **A way to check a graph-wide invariant at commit time.** ADR 0018's property validators
   (`validateProps`) are deliberately pure and single-node: no graph queries, no counting other nodes. They
   cannot express "does at least one other `User` still hold the admin role."

A third, narrower problem surfaced while designing the `User` lifecycle: the engine's existing rule that a
change must leave every lifecycle-typed node in a non-editable state (`checkChangeImpacts`,
`pkg/graph/changeimpact_apply.go`) is correct for a review-workflow lifecycle (`Requirement`, `Release`: a
transient `draft` state a change must move the node out of before landing) but wrong for a status-like
lifecycle, where the normal resting state (`active`) is also the state a node's properties should stay freely
editable in. The rule was unconditional; `User` needed it to not apply.

## Decision

### NodeValidator: a commit-time, graph-query-capable extension point

`pkg/graph/validator.go` adds `NodeValidator`, a Go-level plugin interface distinct from ADR 0018's property
validators:

```go
type NodeValidator interface {
    Types() []string
    Validate(ctx context.Context, q ValidatorQuery, impacted []ValidatedNode) error
}
```

- **Dispatch is by the change's own impact list**, not a global scan: `Validate` only runs when a change
  actually touches one of the types a validator names in `Types()`.
- **`ValidatorQuery` reads the graph as the change leaves it** — bound to the exact node-version map (`target`)
  `Apply` is about to persist as the new baseline, not a branch read, since an impacted node's new version only
  joins its branch after `Apply` advances it, which happens after validators run.
- **Invocation point**: `Graph.applyTx`, immediately after `checkChangeImpacts` succeeds and before
  `tx.PutBaseline`, inside the same transaction — a rejection aborts the whole `Apply` atomically, the same
  guarantee property validators and the editable-state check already give.
- **No nested transaction**: `ValidatorQuery` is served directly off the `Tx` `Apply` already holds (the stores
  are not reentrant, same constraint `authorizeMoves` already documents) — it cannot call back into
  `Graph.NodesOfType` or similar convenience methods that open their own transaction.
- Registered on `Graph.Validators []NodeValidator`, the same way `Graph.Authorizer` is: unset, no plugin
  validators run (tests, tools).

### RestInEditable: the editable-exit rule becomes a per-lifecycle declaration

`domain.Lifecycle` gains `RestInEditable bool` (default `false`, preserving every lifecycle declared before
this field existed with no YAML change). `checkChangeImpacts`'s existing check —
"a change leaves nodes in an editable state" — now skips a lifecycle that declares it:

```go
if lc.Editable(n.State) && !lc.RestInEditable {
    editable = append(editable, ...)
}
```

This is a property of the *lifecycle* (declared at the `NodeType`'s own definition, the same YAML block that
names its states and transitions), not a hardcoded engine rule — a lifecycle whose editable states are
ordinary long-lived statuses, not a review draft, opts out; `Requirement`/`Release`/`maturity` need no change
and keep the original invariant.

### The user lifecycle

`organisation.yaml`'s `User` gets `lifecycle: user`:

```yaml
lifecycles:
  - name: user
    initial: proposed
    restInEditable: true
    states:
      - {name: proposed, editable: true, description: "Created, not yet activated"}
      - {name: active, editable: true, description: "Usable account"}
      - {name: deactivated, description: "Suspended by an admin; read-only until reactivated"}
    transitions:
      - {name: activate, from: proposed, to: active}
      - {name: deactivate, from: active, to: deactivated, permission: "user:deactivate"}
      - {name: reactivate, from: deactivated, to: active, permission: "user:reactivate"}
```

`proposed` and `active` are both editable (a `User`'s profile — `displayName`, `email`, ... — stays freely
editable while active, exactly as before this ADR); `deactivated` is the only non-editable (frozen) state.
`restInEditable: true` is what makes this legal: without it, no change landing a `User` as `active` (the
overwhelming common case) could ever apply, since the unmodified engine rule refuses to leave any node resting
in an editable state. Both `deactivate` and `reactivate` require the `admin` role — not just `reactivate`:
leaving `deactivate` open would let any project member holding `node:transition` suspend arbitrary accounts
under the default policies. No new ABAC policy is needed; the floor rule
(`hasRole(admin) → "*","*"`, ADR 0020) already grants both.

`graphsvc.createUser` (ADR 0040/0042's first-sign-in bootstrap) activates the new `User` in the same commit
that creates it (`NodeEdit.State: "active"`, a new field on `NodeEdit` mirroring `NodeWrite.State`), under an
**anonymous** authorization context (`authz.With(ctx, authz.Principal{})`) rather than the signing-in caller's
own identity: `TransitionAuthorizer` already trusts an anonymous caller as "an internal service," and a
brand-new subject holds no role or project yet — running the activation under their own identity would deny
`node:transition` for every single new user, not just unusual ones. This is a real bug the lifecycle addition
would otherwise have introduced, caught and fixed as part of this change, not a hypothetical. `SeedUser`
(tooling/tests) gets the same treatment: a seeded `User` left `proposed` would silently never count toward the
admin floor until some unrelated later commit happened to touch a `User` or `Assignment`.

### AdminFloorValidator

`pkg/access.AdminFloorValidator` is the first (and so far only) `NodeValidator`. `Types()` returns `User` and
`Assignment` — either can remove the last administrator (deactivating the `User`, or retiring/narrowing their
platform Assignment). `Validate` loads every `User` and `Assignment` the change leaves live, finds `User`s
linked `assigns_org` from a **platform** Assignment (`assigns_org`, no `assigns_project`, ADR 0046) carrying
`RoleAdmin` (ADR 0047), and accepts if at least one such `User` is `active`.
Wired onto `Graph.Validators` in `cmd/goap-dev` and `cmd/graph`, next to where `Graph.Authorizer` is wired.

## Consequences

- A change that would leave the graph with no active administrator is refused at `Apply`, atomically, with the
  graph untouched (the change is left `abandoned` the way any other `ErrInvalid` commit failure leaves it).
- `User` profile edits are unaffected by any of this: `active`/`proposed` stay freely editable, exactly as
  before the lifecycle existed.
- `NodeValidator` is a new, reusable extension point for any future graph-wide invariant that needs real query
  access (not just this one): registered per `Graph`, dispatched by the change's own impact list.
- `RestInEditable` is additive and opt-in; no existing lifecycle (`requirement`, `release`, `maturity`) needs a
  YAML change.
- **Not done**: the registry's proto round-trip (`internal/registrysvc/convert.go`'s `lifecyclesToPB`/
  `lifecyclesFromPB`) does not yet carry `RestInEditable` — a user-defined domain published through the web
  Domain editor cannot use it yet. Only builtin domains (`organisation`, `platform`, `methodology`), which load
  straight from YAML and never round-trip through the registry, support it today. The web Domain editor also
  does not yet surface a domain's `lifecycles:` section in its navigation at all (a pre-existing gap, not
  introduced by this ADR, but relevant to anyone who goes looking for the `user` lifecycle there).
