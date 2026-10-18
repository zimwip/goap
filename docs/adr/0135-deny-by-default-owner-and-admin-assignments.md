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

- **A missing authorizer refuses.** `authz.Check(nil, …)` returns `ErrForbidden`; each graph hook refuses when
  nil. A test or the `none` auth mode passes `authz.AllowAll` explicitly. `graphsvc.Boot(RequireHooks)` covers
  every access hook; a composition without a registry (`platform/cmd/graph`) wires an `OperationAuthorizer` that
  allows administrators only.
- **An empty list grants nothing.** `mayRun` with no `Roles` holds for administrators (of the platform, or of the
  resource's project) only; a methodology that wants an agent, an action, a model or an operation open to every
  member says so (`roles: ["*"]`, the explicit "any role on the project"). `Methodology.OperationRoles` restricts
  every operation a role does not list.
- **Every read is authorized.** The graph handler authorizes reads like the index does: a node, `read` on its type
  and access to its project; a change, access to its project (personal changes: their subject only). The
  `r.obj.Org == ""` clause goes: a resource with no org is a platform resource, read by a platform `reader` or an
  administrator, or by a rule naming its type.
- **Membership alone does no work.** The `onProject` rules for writes (`object:create`, `node:transition`,
  `tool:call`, `change:transition`, `change:move`) are removed: these go through the operations of the change
  (`operation:perform`, ADR 0103) and the roles the methodology declares. `onProject` stays for reading a
  project's processes and changes; `prompt:inspect` needs a role listing the operation `prompt.inspect`.
- **Roles come from the graph only.** In `local` mode the gateway ignores the roles of a token; `hs256` takes them
  only with `GOAP_TRUST_TOKEN_ROLES=1` (an external identity provider). `/auth/dev-token` never mints `admin`.
- **No role name in the code.** `release:deploy` and `derogation:sign` lose their rules naming `release_manager` /
  `derogation_signatory`: a methodology declares these roles and the operations they allow.

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
| `owner` | everything `admin` grants, and **possession**: sets and removes the `admin` and `owner` flags; stops the project (retires it through its lifecycle, `config` → `retired`), restores it, moves it in the project tree |

- **Inherited, never taken.** The flags hold on the project the Assignment names and every project below it
  (`ProjectChain`). Creating a sub-project makes nobody its owner: its owners are those of the projects above it,
  until an owner sets the flag on an Assignment of the sub-project. An administrator who creates a sub-project
  administers it through the flag they already hold above.
- **Always an owner.** A project chain keeps at least one owner (an `owner` Assignment on it or above it, to a
  unit or a user); `ProjectOwnerValidator`, next to `AdminFloorValidator`, refuses a commit that leaves a live
  project with none, the root project aside (a platform administrator owns it). A platform administrator
  remains above every project: it sets an owner on an orphaned one.
- **Never beyond the scope.** An owner or an administrator touches neither the platform resources (methodologies,
  domains, policies, organisation units, adapters, models), nor a platform Assignment, nor an Assignment or a
  project outside their chain, nor the project their own flag is held on in a way that removes it from their
  chain (moving it elsewhere is a platform administrator's).
- **Resolution.** `Authorize` sets `Principal.ProjectAdmin` / `ProjectOwner` when an Assignment of the subject's
  chain carrying the flag covers the resource's project chain (resource-dependent, like the project roles); the
  owner flag implies the admin flag. Casbin functions `isProjectAdmin(r.sub)`, `isProjectOwner(r.sub)`.
- **Floor.** Unchanged: `hasRole(r.sub, "admin")`, the platform role. The project flags are default policies: a
  stored policy may narrow them, never a platform administrator.
- **Assignments.** The `adminOnly` gate of `Assignment` asks `assignment:write` with the Assignment's project;
  `assignment:flag` when the edit sets or removes `owner` / `admin`. Default rules: `assignment:write` for
  `isProjectAdmin`, `assignment:flag` for `isProjectOwner` (the platform administrator through the floor).
- **Projects.** The `adminOnly` gate of `ProjectUnit` asks `project:create` / `project:write` with the project
  its `project_part_of` names (`isProjectAdmin`); `project:retire` (a move to `retired` or back) and
  `project:move` (a new `project_part_of`, asked on the old and the new parent) for `isProjectOwner`. The project
  itself is checked on its parent's chain, so a flag held on a project does not let its holder edit that same
  project's place.
- **Checked at commit.** `ProjectScopeValidator` (graph validator, like `AdminFloorValidator`) checks the final
  state of a change touching Assignments or projects as the person committing it: every Assignment and project
  written is in the writer's chain, every flag set or removed by an owner of it, no flag on a platform Assignment.
  The handler gate is the early refusal, the validator the guarantee (a personal change, the settings, `goap-change`
  and the assistant all go through it).
- The platform roles (`admin`, `reader`), `AdminFloorValidator` and the first user's grant (ADR 0040) are
  unchanged.
- **Session.** `access.Session.can` gains, per project of the session, `administerProject` and `ownProject`;
  the Assignments pane offers the flags and the role editing by what the session allows, the Projects tab the
  creation, retirement and move of projects.

## Consequences

- Every shipped methodology (`sdlc`, `release-based`, `risk-management`, `methodology-improvement`, the examples)
  must declare the roles of its agents, actions and operations, `"*"` where every member is meant. A methodology
  that declares none runs for administrators only.
- More refusals at first: the tests that rely on an open authorizer pass `authz.AllowAll` explicitly, which keeps
  the opening visible.
- The database starts from zero: no migration of stored Assignments or policies.

## Plan

1. `authz`: nil refuses, `AllowAll`, `isProjectAdmin` / `isProjectOwner`, the new defaults.
2. `lib/access`: the flags on `Assignment` (domain `organisation` and its attributes), `Authorize`,
   session hints.
3. `graphsvc`: authorized reads, the `assignment:*` and `project:*` gates; `ProjectOwnerValidator`,
   `ProjectScopeValidator`.
4. `mayRun` / `OperationRoles` with `"*"`; the shipped methodologies declare their roles.
5. Gateway: token roles, dev-token.
6. Web: the Assignments pane, the session hints.
7. `docs/architecture.md`, `CLAUDE.md`.
