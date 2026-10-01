# ADR 0044 — Keeping a session: token refresh, expiry and sign-in feedback

**Status**: accepted, implemented · **Date**: 2026-10 ·
Completes ADR 0040 / 0042 (local sign-in).

## Context

A local sign-in token lived 12 hours and nothing renewed it: an active user was dropped mid-work when it expired,
and an expired or refused token (a secret rotated, a memory-mode restart) left the web in a broken state — every
call failing with "authentication required" until the user cleared the token by hand. The sign-in page itself gave
raw HTTP messages and no hint of why the user was back on it.

## Decision

- **Refresh.** `POST /auth/refresh` (local mode) reissues a valid token with a fresh expiry, keeping its subject,
  org, project and roles, and its sign-in time (`auth_time` claim, kept across refreshes and project switches). A
  token lives `Config.TokenTTL` (`GOAP_TOKEN_TTL`, 12h by default); refreshes go on up to `Config.MaxSession`
  (`GOAP_SESSION_MAX`, 7 days) after the sign-in, then the user signs in again. A refresh re-declares the user
  (`OnSignIn`). Register, login, refresh and project switch answer `{token, expiresAt}`.
- **Expired is told from invalid.** The gateway answers 401 `token expired` or `invalid token` (`session expired`
  for a refresh past the maximum session).
- **The web keeps the session** (`web/src/lib/stores/auth.svelte.ts`, local sign-in only): it refreshes the token a
  quarter of its lifetime before it expires (between 30 s and 10 min before), looks again when the tab comes back
  (visibility, focus) since timers pause while a device sleeps, retries a refresh the gateway could not answer, and
  follows the other tabs (storage events). A token that expired, or any request refused with 401 while the current
  token was sent (`api.reportUnauthorized`, from `rpc`, `whoAmI` and the event stream; a response for a token
  replaced meanwhile is ignored), ends the session: the token is cleared and the sign-in page says why.
- **Sign-in page.** It shows that notice, fills in the last subject that signed in, offers to show the password,
  asks for the password twice and checks its length when creating an account, and turns errors into messages
  (wrong subject or password, account already exists, platform unreachable or not ready).

## Consequences

- An idle session survives `GOAP_TOKEN_TTL`; an active one, `GOAP_SESSION_MAX`.
- Tokens are still stateless: signing out clears the client's token only (no server-side revocation, ADR 0040).
