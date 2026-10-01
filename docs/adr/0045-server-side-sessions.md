# ADR 0045 — Server-side sessions: signing out revokes the tokens

**Status**: accepted, implemented · **Date**: 2026-10 ·
Completes ADR 0040 (local auth), ADR 0044 (token refresh).

## Context

Local sign-in tokens were stateless (ADR 0040, 0044): signing out only cleared the browser's copy, and a token
copied elsewhere stayed valid until it expired — and kept being refreshable up to the maximum session. Nothing
ended a user's sessions when their password changed, and a user could not sign out of their other devices.

## Decision

- **A sign-in opens a session**, kept by the credentials service (`internal/credsvc`, table `auth_session` in both
  dialects: id, subject, created, expires — the maximum session —, revoked): `StartSession` at register/login,
  `SessionActive`, `EndSession`, `EndSessions` (Connect `credentials.v1` `StartSession` / `CheckSession` /
  `EndSession` / `EndSessions`). Sessions expired for a day are purged as new ones open. Every token of the
  session carries its id (claim `sid`), kept by refreshes and project switches.
- **The gateway refuses the tokens of an ended session** (local AuthMode): on every request it checks the session,
  trusting a known state for `Config.SessionCheckTTL` (15 s by default) — an ended session never reopens, so that
  state is final; a refresh checks afresh. A token with no `sid` (issued before sessions) is refused: its holder
  signs in again. 401 `session ended`. When the credentials service cannot answer, a session already checked keeps
  its last state and an unchecked one gets a 503 (not a 401: the web does not sign out on an outage).
- **Signing out ends the session** (`POST /auth/logout`, even with an expired token, which still names its session):
  refused at once by the gateway that ended it (the endpoints and the authenticator share the state of the sessions,
  `gateway.Prepare`), within `SessionCheckTTL` by the others. `{"everywhere": true}` ends every session of the
  subject. A new password ends every session of its subject (`credsvc.Service.SetPassword`).
- **The web** offers "Log out on every device" in the profile menu, and tells a session ended elsewhere from one
  that expired on the sign-in page.

## Consequences

- One small read per session every `SessionCheckTTL` per gateway; signing out costs one write.
- A token stolen before its session ended works for at most `SessionCheckTTL` on another gateway.
- Existing local tokens (no `sid`) are refused once after the upgrade.
