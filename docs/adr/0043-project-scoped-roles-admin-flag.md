# ADR 0043 — Roles are held on projects; administration is a flag of the user

**Status**: accepted, implemented (administration is no longer a flag — it became a platform role, ADR 0047) ·
**Date**: 2026-10 ·
Builds on ADR 0020 (access control), ADR 0035 §2 (roles of a methodology), ADR 0039 (project, Assignment,
`methodology@Role`), ADR 0040 / 0042 (user bootstrap, sign-in).

## Context

ADR 0039 introduced projects and Assignments, but kept the older model alongside it: a `User` node listed global
roles (`roles: [admin, contributor, methodologist, approver, release_manager]`, or `role@UNIT` per unit), and the
default policies granted everything but steps from those global roles and the caller's organisation. Project roles
were merged only into the step checks (`step:perform` / `step:approve`). As a result:

- a person had the same rights on every project, whatever they were assigned to;
- the roles of the platform (contributor, methodologist, approver) lived next to the roles of the methodologies, with
  no link between them;
- an Assignment's roles were free text, the project said nothing of the roles it needs;
- the authorizer resolved project roles for the org chain of the *resource* (the unit holding the change), so anyone
  acting on a change of a unit got the roles that unit was assigned;
- agents and actions said nothing of who may run them.

## Decision

- **Administration is a flag of the user** (superseded by ADR 0047: a platform role; the flag is gone). `organisation@User` gets `admin: true` (`access.User.Admin`) instead of
  `roles`; the principal of an administrator carries `admin` (`access.RoleAdmin`), which the floor policy lets do
  everything. The first user is created an administrator (ADR 0040). A node written before lists `admin` among its
  roles: still read as an administrator; its other roles are ignored.
- **Every other role is a methodology's, held on a project.** A methodology declares its roles (`methodology@Role`).
  A project names the methodologies that apply to it, inherited by its sub-projects: their roles are the ones the
  project needs. An Assignment grants some of them to a unit or a user on the project. The authorizer resolves, for
  any request, the roles of the subject — its own `User` node (User extends OrgUnit), then the unit it belongs to and
  its ancestors (`Snapshot.SubjectChain`) — on the resource's project and its ancestors (the root project when the
  resource names none), and merges them into the principal. The same person holds different roles, and may do
  different things, from one project to another.
- **Who may run what is said with those roles.** Steps and methods keep their RACI assignment (ADR 0035). Agents and
  actions gain `roles: [...]` (roles the methodology declares, checked at compile time):
  - an agent is started only by someone holding one of its roles on the project;
  - an action is run when its initiator holds one of its roles — its own, else its agent's; otherwise it waits for an
    approval by someone who does (`HumanTask` of kind approval, permission `action:run`, the allowed roles in `Roles`;
    the approver needs the action's `permission` too); a human action is submitted by someone holding one;
  - with no roles at all, any member of the project (process start already required it).
  The engine acts as the initiator *on the process's project* (`Engine.actor`), so tool calls and model use resolve the
  same roles.
- **Default policies follow the project** (`authz.DefaultPolicies`): an administrator may do everything; holding any
  role on the project (`onProject`) lets one start and act on processes, create objects, transition nodes, call tools,
  and apply changes (never one's own); a release is deployed by a `release_manager` of the project; steps keep
  `hasRoleIn`; agents, actions and restricted models use `mayRun` (one of `r.obj.Roles`, any role when it lists none).
  Methodologies, domains, triggers, the organisation, policies and adapters have no rule of their own: they are
  administered by administrators. The defaults before this ADR are not upgraded (no legacy rule set is kept: the
  project is greenfield); `graphsvc.SeedAccess` seeds the current ones once.
- **Token roles keep their meaning.** Roles a token carries (an external identity provider in `hs256` mode, the service
  identity of a trigger, the fixed dev principal) still count everywhere. A trigger's roles are not checked against the
  methodology's: they may be platform roles (the observer of `methodology-improvement` runs as `admin`, since drafting a
  methodology is administration).
- **The model gateway** resolves the roles a catalog model requires on the caller's project through the authorizer
  (resource `model`, act `use`).
- **The web**: the User tab and the Access screen edit the administrator flag; a project chooses its applicable
  methodologies from the registry and shows the roles it needs, who holds them and which are unassigned; an Assignment
  picks its roles among the project's (`web/src/lib/projectRoles.ts`); the agent and action editors edit `roles`; the
  roles a model can be restricted to are the declared ones.

## Consequences

- A user without any Assignment can sign in and read within their organisation, but starts nothing until an
  administrator assigns them a role on a project (or makes them an administrator).
- Nodes carry no project (one node is shared by the changes of several projects): access to objects is governed by the
  processes, changes and tools of a project, not by a node's own project.
- Not done: an Assignment's roles are not validated server-side against the project's methodologies (the web offers
  only those); an OrgUnit-side restriction of actions on a project (ADR 0039) is still open.
