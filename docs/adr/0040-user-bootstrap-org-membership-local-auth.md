# ADR 0040 — Mandatory org membership, first-user bootstrap, unit parenting, and local auth

**Status**: accepted, implemented · **Date**: 2026-09 ·
Builds on ADR 0020 (access control), ADR 0037 (personal changes and preferences), ADR 0038 (user
preferences service), ADR 0039 (project mirrors organisation; `User extends OrgUnit`; `EnsureUser`).

## Context

`internal/graphsvc.EnsureUser` (ADR 0039) creates a bare `organisation@User` node the first time a subject
is seen — no `member_of` link, no roles. Nothing makes anyone an administrator except a role claimed in a
token or the `GOAP_DEV_ROLES` env var; a fresh, standalone deployment has no path to a first administrator
at all. `OrgUnit`/`ProjectUnit` have no enforced parent beyond the two seeded roots (`ORG-DEFAULT`,
`PROJ-ROOT`): nothing stops a unit from being created rootless by mistake, and nothing lets a unit or a user
be moved to a different parent once created — `part_of`/`project_part_of`/`member_of` are set once, at
creation, with no edit path (`docs/adr` and current UI both confirm this). Finally, the only way into the
system today is a bearer token minted out of band (`POST /auth/dev-token`, explicitly dev-only, no
credential check) and pasted into the web header — a deployment with no external identity provider (no
OIDC/OAuth) has no signup, no login, and no logout.

## Decision

- **`EnsureUser` links `member_of` and bootstraps the first admin.** On first sight of a subject, it links
  the new `User` to `ORG-DEFAULT` (the root of the organisation structure) and, when no other `User` node exists yet in the
  `organisation` namespace, grants `Roles: ["admin"]`. The default org is resolved *before* the node is
  created — `SeedDefaults` can still be seeding at startup (it waits on the registry to publish the type
  catalogue), and creating the node first would leave a permanently broken `User` behind on that race: one
  with no `member_of` and no admin, and worse, one that makes every later subject see the namespace as
  "already has a `User`" and never get the first-admin bootstrap either. `NodesOfType` (the existence check)
  reads live state, not a baseline snapshot: `EnsureUser` writes by import (`CreateNode`/`Link`, outside of
  any change), which does not itself advance a baseline, so a baseline-scoped read would systematically miss
  every user but the ones incidentally folded in by some unrelated later change. A benign race between two
  concurrent first connections can grant admin to more than one subject; it can never grant admin to none,
  which is what matters (the
  existing `Floor` policy already guarantees admins can't be locked out, ADR 0020). `SeedUser` gets the same
  treatment for the seeding path.
- **`member_of` is exactly one link, enforced — at creation, and again after every write.**
  `checkRequiredParent` (`pkg/graph/subchange.go`, parallel to `checkOwnerOrg`) rejects a `User` *creation*
  that would leave it with zero or more than one outgoing `member_of`, checked against the edit's own
  declared links before anything is written. `checkParentInvariant` additionally re-checks a *modified*
  node's actual links right after the write lands: a client can build its edit from a `headGraph`-style
  baseline snapshot that predates a link written by import (`EnsureUser`'s own `member_of`, which — see
  above — does not advance a baseline), so its own view of "the current link to remove" can be empty even
  though one already exists; without this second check such a client would silently leave the node with two
  `member_of` links instead of being told to reload and retry.
- **`OrgUnit`/`ProjectUnit` require a parent, except the two seeded roots.** The same kind of commit-time
  check requires exactly one outgoing `part_of` (`OrgUnit`) or `project_part_of` (`ProjectUnit`) on write,
  except `ORG-DEFAULT`/`PROJ-ROOT` (the roots of the structure tags, ADR 0069) — created once, only by `SeedDefaults`/`seedRootProject`,
  which stay privileged and unaffected. This mirrors the existing "root is special-cased, not
  generically exempted" precedent already set for `PROJ-ROOT`'s self-link (ADR 0039).
- **A new `move` action reassigns membership/parentage.** One administrative action (this namespace's own
  processes are already documented as administrative, `domains/builtin/organisation.yaml`) that, in a single
  Change, removes the existing `member_of`/`part_of`/`project_part_of` link and adds the new one — a unit or
  user is never left, even transiently at rest, with zero or two. Surfaced next to the now-read-only "Part
  of"/"Member of" display in `OrganisationTab.svelte`/`UserTab.svelte` as a "Move to…" control.
- **A third gateway `AuthMode`, `"local"`.** Parallel to `"none"`/`"hs256"` in `authenticator`
  (`internal/gateway/gateway.go`). New endpoints, same shape as the existing `devToken`/`switchProject`
  registrations: `POST /auth/register` (subject + password → creates the `User` through `EnsureUser`,
  including first-admin bootstrap, plus a credential), `POST /auth/login` (subject + password → a JWT signed
  the same way `Claims`/`signToken` already do), `POST /auth/logout` (below).
- **Credentials live in a new, separate service — not the graph, not a User property.** `internal/credsvc`,
  shaped like `internal/prefssvc` (ADR 0038): one record per subject (password hash + algorithm id) in its
  own store, so a failed login never touches the audited graph/change log and no secret material ever
  becomes graph data (design rule 4's exception list gains one entry, alongside domains and service
  config). It sits behind a small `Verify(subject, password) bool` / `SetPassword(subject, password) error`
  interface so the concrete backend can move from its own SQL table today to being `platform.Secrets`/Vault-
  backed later, with no call-site change — the explicit reason for keeping it a separate service rather than
  a graph property from day one.
- **Web signin/signup screens**, shown at boot when there is no token and the gateway reports
  `AuthMode: "local"` (a small unauthenticated `GET /api/auth/config` endpoint, since `whoami` itself needs a
  principal to answer). Reuses the existing token plumbing (`api.ts`, `stores/session.svelte.ts`); replaces
  the pasted-bearer-token box in `Header.svelte` for local mode only — the `hs256`/dev-token box stays for
  operators, `none` shows neither.
- **Logout, on the User profile tab.** `UserTab.svelte` gains a "Log out" action calling `POST
  /auth/logout` and clearing the local token (reusing `Header.svelte`'s `clearToken`). It reads the same
  `AuthMode` the signin screen reads and hides itself when that mode is an SSO one (OIDC/OAuth) — a forward
  seam only: no such mode exists yet, so today this condition never hides it, but adding one later needs no
  change here.

## Known limitations

- **OIDC/OAuth itself is out of scope.** This ADR only prepares the seam (`AuthMode` gates both the signin
  screen and the Logout action); implementing an actual OIDC/OAuth `AuthMode` is left for a later ADR.
- **No server-side token revocation.** HS256 tokens are stateless; `POST /auth/logout` clears the client's
  token only. A real revocation list or short-lived-token-plus-refresh design is future work.
- **First-admin race** can produce more than one administrator on simultaneous first connections; never
  zero. Considered acceptable.

## Consequences

- `docs/architecture.md` §3.9b and the organisation bullet of `CLAUDE.md` need updating once implemented.
- `migrations/` and `migrations_sqlite/` each gain `credsvc`'s schema (both dialects, per repo convention).
- Any direct API caller that creates a bare `OrgUnit`/`ProjectUnit` with no parent (seeding scripts, tests)
  must be audited and given one — this is a breaking change for that narrow path, not for normal usage
  through `OrganisationExplorer.svelte`/`ProjectsExplorer.svelte`, which already collect a parent at creation.
- `EnsureUser`'s cost grows by one existence check (namespace-wide "does any `User` exist") on the very first
  call after a fresh start only; every subsequent call is unaffected (dedup already applies).
