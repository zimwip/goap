# ADR 0046 — Platform roles: an Assignment naming no project

**Status**: accepted, implemented · **Date**: 2026-10 ·
Builds on ADR 0043 (project-scoped roles, admin flag), ADR 0039 (project, Assignment).

## Context

ADR 0043 made every non-administrator role a methodology's, held on a project: an Assignment grants a unit or
user some of the roles its project's applicable methodologies declare. This left a gap: a project that names no
methodology declares no role, so no Assignment can be created for it — the web's role picker is empty and
`save()` refuses an Assignment with no roles. There was no way to grant someone a role that holds regardless of
which methodologies a project applies, short of making them a full administrator (`User.Admin`).

Administration itself already works this way informally: it is not a methodology role, it is a flag the floor
policy checks everywhere. ADR 0043's own text calls a trigger's roles "platform roles" by analogy. What was
missing was a second, lesser one — a reader, say — granted the same way administration is, without promoting
administration itself into a general mechanism.

## Decision

- **A platform role is a fixed, built-in catalog entry of the platform namespace** (`platform@Role`,
  `pkg/mcp.BuiltinRoles`, today just "reader"; `access.RoleReader` names it for the ABAC rules, `access.
  PlatformRoles` lists the grantable names). `SeedBuiltins` keeps its node in sync with the code at every start,
  the same way it does for the built-in MCPs (ADR 0028) — unlike `methodology@Role`, there is no registry version
  for it, it ships with the platform. The node documents the role for the IDE; it is not consulted to decide what
  the role may do (that is `authz.DefaultPolicies`) or to validate an Assignment's roles (not checked
  server-side, same gap as methodology roles, ADR 0043). Administration stays `access.RoleAdmin` / `User.Admin`,
  not one of these.
- **An Assignment naming no project grants one.** The `organisation@Assignment` node type is unchanged; its
  `assigns_project` link is simply optional. An Assignment with `assigns_org` and no `assigns_project` grants
  the platform role(s) in its `roles` to that org unit (or user) everywhere — independent of any project's
  methodologies, because it is never resolved through a project chain at all. Its key is
  `access.PlatformAssignmentKey(org)` ("ASG:\<org\>/PLATFORM"), distinct from any per-project
  `access.AssignmentKey(org, project)`.
- **Resolution is a separate, unconditional merge.** `Snapshot.PlatformRoles(orgChain)` unions the roles of every
  platform-wide Assignment whose org is in the chain; `Authorizer.Authorize` merges it into the principal's roles
  on every request, alongside (not instead of) `ProjectRoles` for the resource's project chain. A platform role
  is not scoped to a project by the grant itself — a policy that wants it scoped reads the project's own
  Assignments itself (the existing `onProject`/`ProjectRoles` machinery), the same way any other rule can narrow
  what a role is allowed to do.
- **The one shipped policy**: `hasRole(r.sub, "reader")` allows `read` on everything, ahead of (and wider than)
  the existing org/project-scoped read rule. (Administration was later migrated into this same mechanism, ADR
  0047 — not part of this ADR as first written.)
- **The web**: `AssignmentsPane.svelte`'s project selector gains a "platform-wide (no project)" choice; picking
  it offers the built-in platform roles (`web/src/lib/projectRoles.ts`'s `PLATFORM_ROLES`, mirroring
  `access.PlatformRoles`) instead of a project's methodology roles, and saves an Assignment with no
  `assigns_project` link. A project with no applicable methodology now shows that choice as the way to grant
  someone access to it, instead of a dead end. The pane falls back to the same platform-role picker whenever
  the chosen (or, since the project selector is hidden there, the *fixed*) project offers no role of its own —
  not only when "platform-wide" is explicitly picked — so creating an Assignment from a methodology-less
  project's own tab (`ProjectTab.svelte`, which opens the pane with `fixedProject` set and no project selector
  at all) is not the dead end it still was at first: the granted role holds everywhere, not just on that
  project, which the pane says so inline.

## Consequences

- A project needing no methodology-specific role (a holding area, a project still being set up) is no longer a
  trap: a platform role reaches it like it reaches everything else.
- Platform roles stay a short, built-in catalog curated in code (`pkg/mcp.BuiltinRoles`), not something a
  methodology or a project can extend — deliberately narrower than `methodology@Role`, to keep "what a role can
  do platform-wide" reviewable in one place rather than spread across methodologies.
- A policy wanting a platform role scoped to particular projects writes that scoping itself (reads the project's
  Assignments); the grant mechanism does not do it for you.
