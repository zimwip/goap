# ADR 0052 — The web keeps the session in one place and the location in the URL

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0040, 0042, 0044, 0045.

## Context

After a database reset and a new sign-in, the web reopened the tabs, project and panel the user had before: the
workbench state lived in `localStorage` (`goap.ide.tabs`, `goap.project`, ...) and was restored at import, before
the sign-in. A token whose server session had vanished still looked valid to the browser, and the 401 handling
(`onUnauthorized`, cross-tab sync) started only when `/api/auth/config` answered and said `local`, so a gateway that
was down at boot left the app with a dead token until a manual reload.

## Decision

- **The URL is the location.** `shell/router.ts`: the active tab is `/<kind>?<param>=<value>`, home is `/`.
  Tabs live in memory; the active one follows the address (`tabs.svelte.ts`: `startRouting`, called by the shell
  once the views are registered). Back / forward, reload and shared links work; a new page opens on home.
  Panel selection, explorer expansion and baseline selection are no longer stored.
- **The token claim is the project.** `goap.project` is gone; `project.current` follows the identity.
- **A session change restarts the page.** Signing in or out (the token appearing or disappearing, in this tab or
  another) drops what the browser kept for the user (`forgetSessionState`: notifications, assistant), then reloads
  on home: no store keeps the previous user's data. A refreshed or reissued token is not a session change. The
  reason of an ended session survives the reload as `goap.notice`, read once by the sign-in page.
- **One session keeper for every mode.** A 401 on the current token (including an end-of-stream refusal and the
  project switch) ends the session whatever the mode; only the refresh is local-only. `loadAuthConfig` retries
  while the gateway is unreachable instead of assuming no sign-in.
- `refreshIdentity` ignores a stale answer and keeps the real error.

What the browser still keeps: the token, the last subject, layout sizes and theme, filters, voice settings.
