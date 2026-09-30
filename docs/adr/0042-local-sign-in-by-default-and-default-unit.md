# ADR 0042 — Local sign-in by default, users declared at sign-in, the default unit flag

**Status**: accepted, implemented · **Date**: 2026-09 ·
Completes ADR 0040 (user bootstrap, local auth), builds on ADR 0039 (`EnsureUser`, `User extends OrgUnit`).

## Context

ADR 0040 gave the platform its own sign-in (`AuthMode` `"local"`: register, login, logout backed by
`internal/credsvc`), but left it opt-in: both `cmd/gateway` and `cmd/goap-dev` defaulted to `"none"`, where
every caller is a fixed dev principal. Three gaps remained to make users a real part of the system:

- **Sign-in was not the default.** A deployment with no identity provider still had to know to turn it on.
- **Users were declared late and by import.** The `User` node was created on the first graph call after
  sign-in (`EnsureCaller`), written outside of any change (`CreateNode` + `Link`, not atomic). An import
  write does not move the head of `main`, and `pkg/access` reads roles and units from that head: a new user,
  including the first administrator, got neither their unit nor their roles until some unrelated change
  happened to fold them in. It also broke design rule 2 (every modification is a change).
- **The unit new users join was hardcoded** to `ORG-DEFAULT`, the root of adapter resolution — two different
  responsibilities on one node, with no way for an organisation to say "newcomers go to *this* unit".
- Smaller: the profile menu had no sign-out (only the user tab did, as a toolbar action), still showed the
  pasted-token box in every mode, and switching project (`POST /auth/dev-token/project`, ADR 0039) was only
  mounted for dev tokens, so a locally signed-in user could not change project.

## Decision

- **Local sign-in is the default** (`gateway.DefaultAuthMode = "local"`, `GOAP_AUTH_MODE` unset) in the
  gateway, in `goap-dev` and in the compose file. It stays on unless an external identity provider (SSO)
  issues the tokens: `hs256` (tokens minted elsewhere) today, an OIDC mode later. `none` (no sign-in, the
  fixed `GOAP_DEV_*` principal) is for development and must be asked for explicitly. `goap-dev` needs no
  configured secret for it: without `GOAP_JWT_SECRET` (or Vault) it generates one, kept next to the SQLite
  database (`.goap/jwt_secret`, mode 0600) so a restart signs no one out, or per process in memory mode.
- **A user is declared at sign-in.** `gateway.Config.OnSignIn` runs after a successful register or login,
  before the token is signed; a failure refuses the sign-in (503). `goap-dev` calls `graphsvc.EnsureUser`;
  the gateway, which has no graph in process, calls `graphsvc.Client.DeclareUser`: a `GetNode` of the user's
  key made as that subject, which the graph service's `EnsureCaller` interceptor answers by creating the node
  first — finding it proves it exists. Both then refresh their `access.Directory` (`Refresh`: past its
  one-second TTL) so the very first calls carry the user's unit and roles. `EnsureCaller` still covers
  callers that never went through sign-in (hs256 tokens).
- **`EnsureUser` commits a change.** The `User` node and its `member_of` link are one commit on `main`
  (`createUser`, the same `applyOn` the seeds use): atomic, journaled, and it moves the head, so the access
  snapshot sees the user at once. Creations are serialized within a process (the web fires several calls at
  once right after signing in) and retried when another change moved `main` meanwhile (`ErrConflict`).
- **The default unit is a flag.** `OrgUnit` gains a `default` property (`access.PropDefaultUnit`,
  `organisation` domain 1.6.0): new users join the unit that carries it (`graphsvc.DefaultUnit`, read from
  live state). `SeedDefaults` sets it on `ORG-DEFAULT`; with no flagged unit (a graph seeded before the flag)
  `ORG-DEFAULT` is used, and with several (two concurrent changes each moving it) the smallest key wins, so
  the answer stays deterministic. The Organisation tab shows which unit new users join and offers "Make
  default", which moves the flag in one change (set on this unit, cleared on every unit carrying it). Existing
  users stay where they are; `ORG-DEFAULT` remains the root of adapter resolution whatever carries the flag.
- **Profile menu.** The header's user menu offers "My profile" (the user's own tab) and, with local sign-in,
  "Log out" (`POST /auth/logout`, then the sign-in page replaces the shell). The pasted-token box shows only
  in `hs256` mode. The user tab's own "Log out" action follows the same rule (`signsInLocally`), so an SSO
  mode signs out through its provider.
- **Switching project reissues the token in local mode too** (`/auth/dev-token/project` mounted for
  `"local"`).

## Consequences

- A fresh deployment opens on the sign-in page; the first account created is its administrator (ADR 0040).
  Development setups that relied on the implicit dev principal set `GOAP_AUTH_MODE=none`.
- Every new user is a change of the `organisation` namespace ("User <subject>"), visible in its history.
- Not implemented (unchanged from ADR 0040): an OIDC/OAuth mode, server-side token revocation.
