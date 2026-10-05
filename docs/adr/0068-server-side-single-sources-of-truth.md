# ADR 0068 — Server-side single sources of truth: the admin-only flag, one chain resolver, auth strategies, opaque preferences

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0020, 0038, 0040, 0042, 0045, 0054, 0055, 0065.

## Context

Four pieces of knowledge lived in two or three places, each a list or a branch that had to be kept in step by hand:
the node types only administrators write (`access.IsAccessType`, a hardcoded list used by the graph handler and by
`goap-change`), the walk up the organisation (`pkg/access`, `internal/mcpsvc` and `goap-admin` each rebuilt it), the
authentication modes (`switch AuthMode` in the gateway, in two commands and, by name, in the web), and the keys of the
user preferences (a schema in the preferences service mirrored by the web).

## Decision

Greenfield: no shim, the old list, switches and schema are removed.

1. **`adminOnly` on a node type.** A node type of a domain may carry `adminOnly: true` (`def.NodeType.AdminOnly`,
   registry proto `NodeType.admin_only`): its nodes are written by platform administrators only (checked against the
   floor, ADR 0020), never by the ordinary change / object / node rules, and never by a direct write. A subtype
   inherits it (`User extends OrgUnit`). The built-in `organisation` domain flags `OrgUnit`, `Adapter`, `Policy`,
   `ProjectUnit`, `Assignment` (so `User`), the `platform` domain `AdapterDef`. The type catalogue resolves it
   (`typecat.Catalog.AdminOnly`, `graph.TypeCatalog.AdminOnly`) and the graph answers one question,
   `Graph.AdminOnlyType(ctx, typ)`: the catalogue's flag, plus the built-in domains for an untyped graph (`typecat.Builtin()`, ADR 0069). The graph handler (`refuseDirectWrite`, `gateAccess`) and
   `goap-change` (`Change.gate`) both ask it, the connector through `engine.GraphPort.AdminOnlyType`, which the
   in-process graph implements and `graphsvc.Client` implements with the new RPC `IsAdminOnlyType`. A domain
   flagged type is gated with no code naming it. `access.IsAccessType` is gone. `AdminFloorValidator.Types()` stays a
   list: it is not "who may write a type" but the two types that decide who administers (User, Assignment).
2. **One chain resolver.** `domain.Hierarchy` (`pkg/domain/hierarchy.go`), built by
   `domain.Structures.Hierarchy(kind, nodes, links)` from the structures and the nodes / links the graph returns, with
   `Chain(key)` (self first, nearest first, root last, an empty key is the root, cycle-safe), `Parent`, `Root`. It is
   used by `access.Snapshot` (organisation and project chains), `mcpsvc.Snapshot` (the adapter chain) and the `units`
   / `users` listings of `goap-admin`, which no longer rebuilds parents ad hoc nor names the unit type, link or
   `ORG-DEFAULT` (`engine.GraphPort` gained `Structures`). It lives in `pkg/domain`, the leaf both services already
   import, so no layering rule moves.
3. **Auth modes are strategies.** `gateway.Mode` (`Name`, `Open`, `VerifiesTokens`, `Sessions`, `Validate`,
   `Authenticate`, `Routes`), three implementations (`none`, `hs256`, `local`) registered by name
   (`RegisterMode`, `LookupMode`; an unset or unknown name is an error). The gateway looks the mode up once and no
   longer branches on the name: the authenticator calls `Authenticate`, `MountAuthEndpoints` calls `Validate` and
   `Routes`, `checkSession` and `Prepare` ask `Sessions()` (server-side sessions are keyed on the strategy, not on
   `"local"`), `cmd/gateway` loads the secret when `VerifiesTokens()` and the credentials client when `Sessions()`,
   `cmd/goap-dev` skips the gateway when `Open()` and generates its secret when `Sessions()`. `GET /api/auth/config`
   answers `{authMode, signsIn}`; the web reads `signsIn` (`authState.signsIn`) instead of comparing the name to
   `"local"`. Flags and environment variables are unchanged.
4. **Preferences are an opaque document.** The preferences service keeps one JSON object per subject, whatever its
   keys: `Set` merges the patch (`null` clears a key) and refuses a document that is not JSON-encodable or larger than
   `MaxDocumentBytes` (64 KiB). `prefssvc.Schema` and `Validate` are removed; the API (Connect, `google.protobuf.Struct`)
   and the read / write by the subject only are unchanged. The web store validates and defaults its own keys
   (`normalize`), so a new preference touches no backend code. This supersedes the "the service knows the preferences
   it serves" decision of ADR 0038.

## Consequences

- A new access-controlling node type is one flag in its domain; nothing in the handler or the connector changes.
- A new way of signing in (an SSO) is a `Mode` and a `RegisterMode`; the web needs `signsIn` only.
- The hub, the access directory and `goap-admin` can no longer disagree on who a unit's ancestors are.
- An out-of-range preference is the web's to ignore; the service only protects its storage.
