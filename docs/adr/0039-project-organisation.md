# ADR 0039 — Project mirrors Organisation; Assignment is where they meet

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0016 (organisation and sub-changes), ADR 0019 (organisations, MCP, connectors), ADR 0020
(access control), ADR 0023 (methodologies as graph nodes), ADR 0028 (built-in MCPs and connectors),
ADR 0035 (methods, roles, operations, documents).

## Context

Organisation (WHO) answers who may act and the scope of their responsibility. Nothing in the model answers
*what the organisation is working on*: the enterprise's changes have an owner unit, but no notion of the
initiative, programme or project that unit is acting for. Without it, an organisation cannot say "these
roles apply here, for this piece of work" independently of the org chart itself — a contractor, a
cross-functional team, or a unit doing two unrelated projects at once all need the same unit to carry
different applicable roles depending on which project the work belongs to.

## Decision

- **Project mirrors OrgUnit.** `organisation@ProjectUnit` is a node type of the existing `organisation`
  namespace (not a new namespace: the two hierarchies belong together, and the one place they meet —
  Assignment, below — stays inside a single namespace so a single change can write both sides). Projects
  compose hierarchically through `project_part_of` (child → parent, mirroring `part_of`), and a project
  names the methodologies that apply to it (`methodologies: []`), so it inherits their declared role
  vocabulary without redeclaring it.
- **A distinct link name, not a reused `part_of`.** The type catalogue (`pkg/typecat.New`) merges a
  link type declared for several end pairs into "accepts any pair" the second time its name is seen. Naming
  the project hierarchy link `part_of` too would have silently erased `part_of`'s OrgUnit-only constraint;
  it is `project_part_of` instead.
- **The root project is seeded self-linked, not rootless.** `ORG-DEFAULT` is the root of the organisation
  chain by *omitting* its `part_of` link; `PROJ-ROOT` (the `root` of its structure tag, ADR 0069) is instead seeded with a
  `project_part_of` link to itself. A self-link cannot be made in the same commit as the node it targets
  (the commit orderer refuses a cycle among created nodes, `pkg/graph/commit.go`), so seeding is two
  commits: create, then link the new version to itself (`graphsvc.seedRootProject`). Every walk that
  follows the chain already guards against revisiting a node (`Snapshot.Chain`/`Snapshot.ProjectChain`, and
  `projectWithin` mirroring `orgWithin`), so the self-link terminates safely with no special-casing needed.
- **Assignment is the meeting point of organisation and project** — the same shape as `Adapter` (the
  meeting point of organisation, MCP and connector, ADR 0019/0028): `organisation@Assignment` links an
  `OrgUnit` (`assigns_org`) to a `ProjectUnit` (`assigns_project`) and carries the `roles` that unit locally
  holds there, a subset of the roles the project's applicable methodologies declare. This extends design
  rule 1's closed list of meeting points (Adapter, Change) with a third: Assignment.
- **User is declared a subtype of OrgUnit**: `organisation@User` gets `extends: OrgUnit`. The smallest
  organisational unit is a person, so a `User` node satisfies an `OrgUnit`-typed reference — concretely,
  `Assignment.assigns_org` can target a unit, a team, or an individual uniformly (`CheckLink`'s existing
  "a subtype is accepted where its supertype is" rule needed no code change; ADR 0012/0013 already allow
  it). This does **not** fold `User` into the `part_of` unit tree: `member_of` still names a user's home
  unit, unchanged.
- **A user is created automatically, not by an administrator's hand.** The same key
  (`access.UserKey`/`access.PersonalUnit`, `USR:<subject>`) served two purposes before this ADR: the
  `organisation@User` identity node (ADR 0020) and the personal unit that holds a subject's personal
  changes (ADR 0037, created lazily on first `@me`). Since `User` now extends `OrgUnit`, one node serves
  both — `internal/graphsvc.EnsureUser` creates it the first time a subject is seen, called both from
  `resolveOwner` (the `@me` path) and from every authenticated Connect call through the `EnsureCaller`
  interceptor (deduplicated per process: a subject already seen costs one map lookup, not a graph read).
- **Role** (the RACI role a methodology's processes and methods assign, ADR 0035 §2) **is a node type**,
  `methodology@Role`, materialized the same generic way as `Agent`/`Action`/`Goal`/`Process`/`Method`
  (ADR 0023): `internal/registrysvc/defs.go`'s `defKinds` table gained one entry
  (`{kindRole, "roles", "methodology@Role"}`) — the split/join/key logic is fully generic over that table,
  so no other registry code changed. This is additive only: `Assignment.roles` still stores role *names*
  (strings), validated against a methodology's declared roles the way `Pre`/`Effects` already validate
  condition names; linking an Assignment to a specific `Role` node (the first cross-namespace reference
  into the methodology meta-domain) is a larger structural step, left for later if the need arises.
- **Role resolution**: `pkg/access.Snapshot` gained `ProjectChain` (mirrors `Chain`) and `ProjectRoles`
  (unions the `roles` of every `Assignment` whose org is in a given org chain and whose project is in a
  given project chain). `authz.Resource` gained `ProjectID`; `access.Authorizer.Authorize` resolves the
  project chain from it and merges the matching `ProjectRoles` into the subject's roles before evaluating
  the request — unscoped (the org/project membership test already happened), so it rides the existing
  `hasRoleIn`/default step policy (`hasRoleIn(r.sub, r.obj.Role, r.obj)`) with no new Casbin function.
  `pkg/engine` threads a project through `Process.Project` (mirroring `Process.Org`) from
  `StartRequest`/`AttachRequest` through `resolveChange` to `stepAllowed`'s `Resource.ProjectID`.
- **Token / API propagation.** The gateway JWT (`internal/gateway.Claims`) gained a `project` claim,
  propagated as `X-Goap-Project` alongside the existing `X-Goap-Subject/Org/Roles`
  (`internal/identity`). `POST /auth/dev-token/project` reissues the caller's token with the same
  subject/org/roles and a new project — "a token regenerated each time the user changes project" — gated
  behind the same `DevTokens && AuthMode == "hs256"` flag as `/auth/dev-token` (there is no other token
  issuance in the repository yet). `graphsvc.CreateChange` and `enginesvc.StartProcess`/`AttachChange`
  default their `project_id` from `authz.From(ctx).Project` when the request names none, so a caller only
  has to pass one explicitly when acting on a project other than their active one.
- **The project selector** sits in the web header, between the brand and the search box (not in the
  `.right` status cluster: "near goap", the product mark, is more discoverable than buried among status
  icons). It calls the switch-project endpoint (`api.switchProject`) and persists the choice locally
  (`stores/project.svelte.ts`); with no token to reissue (`goap-dev`, `AuthMode: "none"`) the selection
  stays local-display only — `goap-dev` has its own fixed `GOAP_DEV_PROJECT` env var instead, the same way
  `GOAP_DEV_ORG` already works.
- **Navigation**: one left-nav entry, "People & Organisation" (not three), sub-navigated into Organisation
  / Projects / Users panels (`PeopleOrgExplorer.svelte` wrapping the three existing explorer components).
  Users are listed (and pre-provisionable) alongside units and projects. Assignment has no nav entry of its
  own: it is reached from any of the three editors' "Assignments" pane (`AssignmentsPane.svelte`, one
  implementation parametrized by which side — org or project — is fixed) via its own "+ Assignment" action,
  or via each explorer's tree/list context menu ("New assignment"), which opens the entity's tab with that
  pane pre-opened.
- **Enforcement**: "a user must select a project before any action but the administrative ones" is checked
  where a *person starts a process* (`Engine.resolveChange`, only when opening a fresh change — an attach
  to an existing change carries its project already) — not at the raw graph API (`CreateChange` still
  defaults an empty `ProjectID` to the root project, the way an empty `OwnerOrg` already defaults to
  `DefaultOrg`): most direct graph callers are administrative by nature (seeding, org/project/policy/
  adapter management, tests, tools) and would otherwise all need updating for a rule that exists to gate
  *user-initiated work*, not graph plumbing. `Methodology.Administrative` methodologies and
  trigger-initiated processes (`Process.Trigger != ""`, no one at the keyboard to ask) are exempt.

## Known limitation

- **OrgUnit-side action restriction** on a project (mirroring `adapter.Restriction`'s `disabled`/`tools`/
  `deny`/`readOnly` shape, ADR 0028) is not implemented: an `Assignment` grants roles but cannot yet narrow
  which actions a unit may run locally within a project.

## Consequences

- `organisation` is now the namespace of two mirrored hierarchies plus their meeting point
  (`docs/architecture.md` §3.9b); `methodology` gained one more materialized element kind (Role).
- Every existing caller of `CreateChange` (tests, tools, methodologies) keeps working unchanged:
  `ProjectID` is additive, defaulting exactly like `OwnerOrg`/`Namespace` do (the administrative mark became a key of `Change.Data`, ADR 0065) — the
  stricter rule lives one layer up, at `Engine.Start`, where `methodologies/*.yaml` and their tests needed
  (and got) a project to seed and pass.
- `USR:<subject>` keys are no longer ambiguous between "personal unit" and "User node": they are the same
  node, so deleting or restricting one no longer risks leaving the other behind.
