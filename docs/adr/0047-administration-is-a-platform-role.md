# ADR 0047 — Administration is a platform role, not a user flag

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on, and partially revises, ADR 0043 (project-scoped roles, admin flag). Builds on ADR 0046 (platform
roles).

## Context

ADR 0046 introduced platform roles — a fixed, built-in catalog (`platform@Role`), granted by an
`organisation@Assignment` naming no project, resolved the same way everywhere, independent of a project's
methodologies. It deliberately did not touch administration: `User.Admin` stayed the ADR 0043 floor flag,
checked directly off the `User` node rather than through an Assignment.

That left two mechanisms doing the same kind of thing: a flag on the `User` node for the one platform role
that happens to be the floor, and an Assignment for every other platform role. Administration is not special
enough to deserve its own representation — it is a platform role like "reader" is, just one the floor policy
checks. Keeping it as a flag also meant it couldn't be granted to a unit (only a `User`), couldn't be revoked
by removing a node the way every other grant is, and didn't show up next to "reader" in the same Assignment UI.

## Decision

- **"admin" joins `pkg/mcp.BuiltinRoles()`.** It is granted the same way "reader" is: an `organisation@Assignment`
  with `assigns_org` and no `assigns_project`, naming `access.RoleAdmin` ("admin") among its `roles`. It can be
  granted to a unit, not only a single `User` — ADR 0043's `assigns_org` already accepted either (`User` extends
  `OrgUnit`).
- **Platform-role resolution moves into `Snapshot.Enrich`**, out of `Authorizer.Authorize`: `Enrich` is
  resource-independent (it does not see `authz.Resource`), so it is the right place for roles that hold
  everywhere, and — critically — it is the *only* thing `Authorizer.Floor()` calls (`floorAuthorizer.Authorize`
  → `Directory.Enrich` → `Snapshot.Enrich`, never the fuller `Authorize`). Administration reaching the floor
  through an Assignment, not just through `User.Admin`, was the one property this ADR could not regress: a
  stored policy must never be able to deny an administrator (ADR 0020, ADR 0043's own invariant). Moving the
  merge into `Enrich` is what makes that still true — `Authorize` keeps only the resource-specific
  `ProjectRoles` merge, since `Enrich` already covers the platform ones.
- **Platform-role resolution no longer requires a `User` node.** It is computed from `Snapshot.SubjectChain`,
  which falls back to `authz.Principal.Org` when the subject has none — a service identity or a token naming
  only an org now gets its platform (and, through the existing project path, project) roles the same way a
  person with a `User` node does.
- **`User.Admin` stays, read-only, for what was already written.** `UserFromProps` and `Enrich` still honor
  `admin: true` (and, from before ADR 0043, `roles: [admin, ...]`) as a legacy way of being an administrator —
  the same back-compat ADR 0043 already gave the pre-ADR-0043 shape. Nothing new writes it.
- **The first-admin bootstrap grants a platform Assignment, not the flag** (ADR 0040, `graphsvc.createUser`):
  the `User` node and its `assigns_org`-only Assignment (`access.PlatformAssignmentKey`, roles `["admin"]`) are
  committed together, the Assignment's link resolved by `ToKey` to the `User` node created in the same commit —
  there is no chicken-and-egg problem to solve (`createUser` already writes through an ordinary internal commit,
  not a privileged raw path, so creating a second node alongside the first costs nothing extra).
- **Not done**: the web's User tab / Access screen still edit the legacy flag, not an Assignment; migrating it
  to grant "admin" the same way `AssignmentsPane.svelte` grants "reader" is left for later.

## Consequences

- Administration can now be granted to a unit, revoked by deleting a node (the Assignment) instead of flipping
  a property, and shown in the same place every other role is — at the cost of one more kind of node an
  administrator-granting change touches.
- A graph that has never been touched since before this ADR keeps working unchanged: `User.Admin` is still
  read, still makes someone an administrator, still reaches the floor (through `Enrich`).
- `Snapshot.Enrich` now does strictly more work per call (a `PlatformRoles` scan in addition to the `User.Admin`
  check) — bounded by the number of platform Assignments in a snapshot, not expected to matter.
