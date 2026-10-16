# ADR 0048 — NodeValidator plugins, the user lifecycle, and the administrator floor

**Status**: accepted, implemented; `RestInEditable` superseded by ADR 0078 (landable states) · **Date**: 2026-10 ·
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
change must leave every lifecycle-typed node in a non-editable state (then
`checkChangeImpacts`) is correct for a review-workflow lifecycle (`Requirement`, `Release`: a
transient `draft` state a change must move the node out of before landing) but wrong for a status-like
lifecycle, where the normal resting state (`active`) is also the state a node's properties should stay freely
editable in. The rule was unconditional; `User` needed it to not apply.

## Decision

### NodeValidator: a commit-time, graph-query-capable extension point

`graph/graph/validator.go` adds `NodeValidator`, a Go-level plugin interface distinct from ADR 0018's property
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
  the commit is about to persist as its baseline, not a branch read, since an impacted node's new version only
  joins its branch once the branch advances, which happens after validators run.
- **Invocation point**: the commit of the change (`CommitChange`, so `Apply`; `commitOnBranch` in
  `graph/graph/apply.go` since ADR 0102), immediately after the impacts are checked (`walkChangeImpacts`) and before
  `tx.PutBaseline`, inside the same transaction — a rejection aborts the whole commit atomically, the same
  guarantee property validators and the landing checks give.
- **No nested transaction**: `ValidatorQuery` is served directly off the `Tx` the commit already holds (the stores
  are not reentrant, the constraint `authorizeMoves` documented at the time; the transition checks now live in
  `walkChangeImpacts` / `ImpactNodeTransition`) — it cannot call back into
  `Graph.NodesOfType` or similar convenience methods that open their own transaction.
- Registered on `Graph.Validators []NodeValidator`, the same way `Graph.Authorizer` is: unset, no plugin
  validators run (tests, tools).

### RestInEditable (superseded by ADR 0078)

`domain.Lifecycle` gained `RestInEditable bool` (`restInEditable` in YAML, default `false`): the rule "a change
leaves nodes in an editable state" skipped a lifecycle declaring it, so a lifecycle whose editable states were
ordinary long-lived statuses could opt out while `Requirement` / `Release` kept the original invariant. *Since then*
ADR 0078 replaced the `editable` flag by landable states: edits are allowed in any state, a state may be flagged
`notLandable`, and `RestInEditable` is gone.

### The user lifecycle

`organisation.yaml`'s `User` gets `lifecycle: user`:

```yaml
lifecycles:
  - name: user
    initial: proposed
    states:
      - {name: proposed, description: "Created, not yet activated"}
      - {name: active, description: "Usable account"}
      - {name: deactivated, description: "Suspended by an admin until reactivated"}
    transitions:
      - {name: activate, from: proposed, to: active}
      - {name: deactivate, from: active, to: deactivated, permission: "user:deactivate"}
      - {name: reactivate, from: deactivated, to: active, permission: "user:reactivate"}
```

As first written, `proposed` and `active` were editable and `deactivated` frozen, `restInEditable: true` letting a
change land a `User` resting `active`; since ADR 0078 every state of the lifecycle is an ordinary landable state and
a `User`'s profile (`displayName`, `email`, ...) is editable in any of them. Both `deactivate` and `reactivate` require the `admin` role — not just `reactivate`:
leaving `deactivate` open would let any project member holding `node:transition` suspend arbitrary accounts
under the default policies. No new ABAC policy is needed; the floor rule
(`hasRole(admin) → "*","*"`, ADR 0020) already grants both.

`graphsvc.createUser` (ADR 0040/0042's first-sign-in bootstrap) activates the new `User` in the same commit
that creates it (`NodeEdit.State: "active"`, a new field on `NodeEdit` mirroring `NodeWrite.State`), under an
internal identity rather than the signing-in caller's own: a brand-new subject holds no role or project yet —
running the activation under their own identity would deny `node:transition` for every single new user, not just
unusual ones. (It was first an anonymous context, which `TransitionAuthorizer` trusted as an internal service; since
the service identity work the authorizers refuse an anonymous context and it runs as the named system principal,
`graphsvc.System` / `SystemPrincipal`.) This is a real bug the lifecycle addition
would otherwise have introduced, caught and fixed as part of this change, not a hypothetical. `SeedUser`
(tooling/tests) gets the same treatment: a seeded `User` left `proposed` would silently never count toward the
admin floor until some unrelated later commit happened to touch a `User` or `Assignment`.

### AdminFloorValidator

`pkg/access.AdminFloorValidator` is the first `NodeValidator` (`llmcfg.ProtectedAliasValidator`, ADR 0084, is the
other). `Types()` returns `User` and `Assignment` — either can remove the last administrator (deactivating the
`User`, or retiring/narrowing their platform Assignment). `Validate` builds the snapshot of the `User`s,
`Assignment`s and units the change leaves live, counts the users holding `RoleAdmin` the way `Snapshot.Enrich`
grants it (a **platform** Assignment, ADR 0046 / 0047, of the user or of a unit above it), and accepts if at least
one of them is `active`. Wired onto `Graph.Validators` in `platform/cmd/goap-dev` and `platform/cmd/graph`.

## Consequences

- A change that would leave the graph with no active administrator is refused at its commit, atomically, with the
  graph untouched.
- `User` profile edits are unaffected by any of this.
- `NodeValidator` is a new, reusable extension point for any future graph-wide invariant that needs real query
  access (not just this one): registered per `Graph`, dispatched by the change's own impact list.
