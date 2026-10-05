# ADR 0070 — The session: the server publishes what the identity implies, the web consumes it

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0020, 0046, 0047, 0052, 0054, 0069.

## Context

`GET /api/whoami` returned the principal and nothing else, so the web mirrored what the server knows: the qualified
types and links of the organisation (`orgTypes.ts`), the roots `ORG-DEFAULT` / `PROJ-ROOT`, the waiting and default
properties, the key schemes (`USR:`, `ASG:<org>/<project>`, `ASG:<org>/PLATFORM`, `POL:`), the built-in platform roles
(`PLATFORM_ROLES`), the meta-namespace to leave out of the views, `NS = 'organisation'` redeclared in ten files, and
gates written as role names (`hasAnyRole('admin')`, and an `approver` role that has not existed since ADR 0043).

## Decision

`whoami` answers a **session**: the principal's own fields (`subject`, `org`, `project`, `roles`) plus what is derived
from them (`access.Session`, built by `access.Authorizer.Session` from the cached snapshot of the directory, so it is as
cheap as `Enrich`):

- `structures`: the organisation and project structures (kind, node type, parent link, root, `default` property,
  types), the data of `GetStructures`.
- `names`: the namespaces (`organisation`, `platform`, `meta`), the node and link types, the key prefixes
  (`user`, `assignment`, `platformScope`, `policy`), the admin role name, the waiting property. Units and projects
  come from the structures; the rest are the constants of `pkg/access`, `pkg/domain`, `pkg/mcp` (`access.NamesOf`).
- `platformRoles`: `access.BuiltinRoles`.
- `can`: hints for the UI, derived by the authorizer (never from a role name), the server still enforces every call.
  `administer`: the compiled-in floor allows the caller everything: it shows the controls only administrators use
  (the organisation's waiting unit, the default project, the model catalogue and quotas, preferences of the platform,
  the usage of the whole platform, the reindex button of the search). `approve`: the caller works on their active project (a platform
  role, or a role an Assignment grants on it or above, `Snapshot.MayAccessProject`), so an approval of someone else's
  run may be theirs; it gates the approval notifications, replacing the `approver` role check (the engine decides, run
  by run, with `step:approve` and `action:run`). There is no separate `admin` field: `can.administer` is the one.

An unreadable organisation is a 503 (the web retries), not a session with no structures.

The web holds the payload in `stores/session.svelte.ts` (the store of the identity, loaded before the shell is shown,
at every token change: refresh, project switch; a sign-in or out reloads the page, ADR 0052) and reads it through
accessors: `types`, `links`, `ns`, `isMeta`, `userKey` / `isUserKey` / `subjectOfKey`, `assignmentKey(org, project?)`,
`defaultOrg`, `rootProject`, `newUserUnit`, `defaultProject`, `platformRoles`, `adminRole`, `can`. `orgTypes.ts` is gone,
`access.ts` and `projectRoles.ts` keep logic only. `pkg/access/session_guard_test.go` fails when web code builds a
`USR:` / `ASG:` / `POL:` key, names a root, a `organisation@` type, a `const NS = '...'`, or gates on `hasAnyRole('...')`.

Policy keys: the editor still gives a new policy a random suffix after the server's prefix. The server's own key (a
digest of the rule, `access.PolicyKey`) names the policies it seeds; nothing derives a key from a rule, and the key of an
edited policy never changes, so the two never have to agree.

## Consequences

- A new built-in type, key scheme or platform role reaches the web by being added to `access.NamesOf` / `BuiltinRoles`.
- Both deployments answer it: the gateway (`gateway.Config.Session`) and `goap-dev`.
- Not yet: the platform-domain types of the model configuration (`platform@LlmProvider`, ...) and the adapter definition
  type are still named in `llmEdit.ts` / `adapterDef.ts`; the web has no consumer of `can` beyond the two flags.
