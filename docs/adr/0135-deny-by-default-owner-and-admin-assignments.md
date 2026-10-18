# ADR 0135 — Deny by default; project owners and administrators

**Status**: proposed · **Date**: 2026-10 ·
Revises ADR 0020 (access control) and ADR 0043 (project-scoped roles); keeps ADR 0046 / 0047 (platform roles,
platform administration). Builds on ADR 0039 (projects, Assignments) and ADR 0103 (operation catalogue).

## Context

A review of the access rules (October 2026) found that many paths **allow when nothing says so**:

| Gap | Where |
|---|---|
| A nil authorizer allows | `authz.Check(nil, …)` returns nil; every nil hook of the graph (`OperationAuthorizer`, `ItemAuthorizer`, `SeatAuthorizer`, …) lets the call through; `platform/cmd/graph` wires no `OperationAuthorizer` |
| Graph reads are not authorized | `GetNode`, `GetChange`, `ListChanges`, the baselines: only identification and the personal changes filter apply; the index filters by project, a direct read does not |
| A resource with no org is readable by anyone | default read rule `r.obj.Org == "" \|\| …` |
| An empty list grants | `mayRun` with no `Roles`: any member of the project; a methodology whose roles list no `operations` restricts nothing (`Methodology.OperationRoles`) |
| Any role on the project is enough | `onProject` rules for `process`, `object:create`, `node:transition`, `tool:call`, `change:transition`, `change:move`, `prompt:inspect` |
| Token roles are trusted as they come | `X-Goap-Roles` from the JWT, `/auth/dev-token` mints any role, `admin` included |
| Role names in the code | `release_manager`, `derogation_signatory` in `authz.DefaultPolicies` |

Members are managed by administrators only (`organisation@Assignment` is `adminOnly`): a project cannot manage its
own people, and "platform role" means both *a role of the fixed catalogue* and *granted everywhere*, so no
structural right can be held on one project.

## Decision

### 1. Deny by default

**Without an explicit grant, everything is refused.** An explicit grant is an Assignment (a role, a flag) matched
by a rule, or the platform administrator floor. Consequences:

- **A missing authorizer refuses.** `authz.Check(nil, …)` returns `ErrForbidden`; so do the graph service's
  builders of the graph hooks (`graphsvc.OperationAuthorizer`, `ItemAuthorizer`, `SeatAuthorizer`,
  `ProjectMoveGate`, …) and the handlers of the services when given no authorizer. A test passes `authz.AllowAll`
  explicitly, which keeps the opening visible. The hooks of the graph core stay nil when not plugged (a library
  default, the core knows no ABAC); `graphsvc.Boot(RequireHooks)` refuses a composition that leaves one of them
  unplugged. `platform/cmd/graph` plugs an `OperationAuthorizer` over the registry (`GOAP_REGISTRY_URL`), else one
  that allows administrators only.
- **An empty list grants nothing.** `mayRun` with no `Roles` holds for administrators (of the platform, or of the
  resource's project) only; `"*"` (`authz.AnyRole`) is the explicit "any role on the project".
  `Methodology.OperationRoles` always restricts: the roles listing an operation (or its kind) allow it, a role
  listing none allows every operation, and a methodology declaring no role leaves its operations to the
  administrators. An agent or an action that declares no role adds no check of its own to the permission of the
  run (`process:start`, held by the members of the project): the engine asks `mayRun` only for declared roles.
- **Every read is authorized.** The graph handler authorizes reads like the index does: a node, access to its
  project; a change, access to its project (personal changes: their subject only); the lists of changes leave out
  what the caller may not read. The `r.obj.Org == ""` clause goes: a resource with no org is a platform resource,
  read by a platform `reader` or an administrator, or by a rule naming its type (the triggers, the types of a
  namespace).
- **Membership is a project role.** A platform role (`reader`) no longer makes its holder a member of every
  project: `Snapshot.Enrich` keeps the platform roles apart (`Principal.Global`, `HasRole` reads both), and
  `onProject` holds for a role held on the project or a flag (§2). The `onProject` rules of the defaults stay for
  now (see *Not yet*).
- **Roles come from the graph only.** In `local` mode the gateway ignores the roles of a token; `hs256` takes them
  only with `GOAP_TRUST_TOKEN_ROLES=1` (an external identity provider, `Config.TrustTokenRoles`).
  `/auth/dev-token` never mints a platform role. The identity headers carry the roles held on the project and the
  platform roles (`Principal.AllRoles`), told apart again by `Enrich`.
- **A stopped project takes no new change.** `CreateChange` refuses a change in a project that is stopped or below
  a stopped one (`Snapshot.ProjectStopped`).

### 2. `owner` and `admin` flags on a project Assignment

Platform administration is unchanged: the platform role `admin` (ADR 0047), granted by an Assignment naming no
project, checked by the floor. The **project** Assignment (`assigns_project` set) gains two boolean attributes,
`owner` and `admin` (default false); a platform Assignment carrying either is refused. Roles stay what the
methodologies declare; the rights on the project are flags of the grant. **The owner possesses the project, the
administrator administers it for them**: the owner holds every right of the administrator, and the rights that
come from possession.

| Flag on a project Assignment | Grants, on the project and its sub-projects (never on the platform) |
|---|---|
| `admin` | **administrator of the project**: every action on a resource of the project chain; manages the Assignments' roles (not their flags); creates and edits sub-projects |
| `owner` | everything `admin` grants, and **possession**: sets and removes the `admin` and `owner` flags; stops the project and restarts it (the `stopped` attribute of `ProjectUnit`: a structure type has no lifecycle), moves it in the project tree |

- **Inherited, never taken.** The flags hold on the project the Assignment names and every project below it
  (`ProjectChain`). Creating a sub-project makes nobody its owner: its owners are those of the projects above it,
  until an owner sets the flag on an Assignment of the sub-project. An administrator who creates a sub-project
  administers it through the flag they already hold above.
- **Always an owner.** A project that has an owner (an `owner` Assignment on it or above it, to a unit or a user)
  keeps one: `ProjectOwnerValidator`, next to `AdminFloorValidator`, refuses a commit that takes away the last
  owner of a project still in the graph (a hand-off in one change passes). A project never owned is a platform
  administrator's alone, and asked for none; a platform administrator remains above every project and sets an
  owner on it.
- **Never beyond the scope.** An owner or an administrator touches neither the platform resources (methodologies,
  domains, policies, organisation units, adapters, models), nor a platform Assignment, nor an Assignment or a
  project outside their chain, nor the project their own flag is held on in a way that removes it from their
  chain (moving it elsewhere is a platform administrator's).
- **Resolution.** `Authorize` sets `Principal.ProjectAdmin` / `ProjectOwner` when an Assignment of the subject's
  chain carrying the flag covers the resource's project chain (resource-dependent, like the project roles); the
  owner flag implies the admin flag. Casbin functions `isProjectAdmin(r.sub)`, `isProjectOwner(r.sub)`.
- **Floor.** Unchanged: `hasRole(r.sub, "admin")`, the platform role. The project flags are default policies: a
  stored policy may narrow them, never a platform administrator.
- **Assignments and projects.** The `adminOnly` gate of `Assignment` and `ProjectUnit` still asks the floor; for
  anyone else it asks `assignment:write` / `project:write` on their active project (`isProjectAdmin`): the early
  refusal. The policy rules name what possession adds (`assignment:flag`, `project:stop`, `project:move`, denied to
  anyone but an owner or a platform administrator), for whoever asks them.
- **Checked at commit.** `ProjectScopeValidator` (graph validator, like `AdminFloorValidator`) judges every write of
  an Assignment or a project as the person committing it, against the state the change started from: a platform
  Assignment and a root project are a platform administrator's; an Assignment of a project, its administrators'
  (on the project before and after the edit), its flags its owners'; a new project, the administrators' of its
  parent; an edit of a project, its administrators'; stopping or restarting it, its owners'; its move, the owners'
  of its old and its new parent. A platform Assignment carrying a flag is refused whoever writes it. A `system:`
  principal acting by itself and a platform administrator are not asked. The handler gate is the early refusal,
  the validator the guarantee (a personal change, the settings, `goap-change` and the assistant all commit). Both
  validators read through the query of the commit only (the stores are not reentrant): the state before is the
  one the change leaves with each node it wrote taken back to its previous version.
- The platform roles (`admin`, `reader`), `AdminFloorValidator` and the first user's grant (ADR 0040) are
  unchanged.
- **Session.** `access.Session.can` gains `administerProject` and `ownProject`, for the active project of the
  session (a platform administrator holds both); the Assignments pane offers the owner and admin flags to an owner
  (an Assignment may carry a flag and no role), the Project tab stops and restarts the project. Hints only: the
  validator decides.

## Consequences

- A methodology that wants an operation, an agent, an action, a model or a seat open to every member says so
  (`roles: ["*"]` where a list of roles is asked); one that declares no role leaves its operations to the
  administrators.
- More refusals at first: the tests that relied on an open authorizer pass `authz.AllowAll` (or
  `graphsvc.EveryProject`) explicitly, which keeps the opening visible.
- The database starts from zero: no migration of stored Assignments or policies.

## Not yet

- The `onProject` rules for writes (`object:create`, `node:transition`, `tool:call`, `change:transition`,
  `change:move`, `prompt:inspect`) and the rules naming `release_manager` / `derogation_signatory`: removing them
  needs the shipped methodologies to declare the operations of their roles first.
- The model gateway lets every caller use a model listing no role; the baselines and the graph-wide reads
  (`BaselineGraph`, the diffs) are not filtered by project.
- `engine.AuthzScope` with a nil `Authz` still grants everything (a library default of the engine's tests); the
  compositions plug the access authorizer.

A link names the version its target had: once a project is edited, its sub-projects' `project_part_of` and its
Assignments' `assigns_project` are suspect links. The access and hub snapshots and the web's head graph read them
too (`graphsnap.BaselineWithSuspects`), so an edited parent keeps its sub-projects and its Assignments.
