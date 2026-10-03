// Which sign-in UI the gateway wants (ADR 0040): fetched once at boot, before any token exists. With the
// platform's own sign-in, it also keeps the session: the token is refreshed before it expires, and a token that
// expired or is refused ends the session with a notice the sign-in page shows.
import { authConfig, getToken, setToken, onTokenChange, onUnauthorized, refreshToken, tokenClaims, RpcError } from '../api';
import { forgetSessionState } from '../shell/storage';

const NOTICE_KEY = 'goap.notice';

/** Reads (and forgets) the reason the previous page left the session: it survives the reload ending it. */
function takeNotice(): string {
  try {
    const n = localStorage.getItem(NOTICE_KEY) ?? '';
    localStorage.removeItem(NOTICE_KEY);
    return n;
  } catch {
    return '';
  }
}

export const authState = $state({
  mode: '',
  loaded: false,
  /** the gateway could not be reached to tell the sign-in mode: retrying */
  unreachable: false,
  /** why the user is back on the sign-in page ('' when they signed out themselves or never signed in) */
  notice: takeNotice(),
});

/**
 * Whether a mode is the platform's own sign-in (ADR 0040, 0042: "local", the default unless an external
 * identity provider is in place): the sign-in page, and signing out from the profile menu. An SSO mode
 * signs in and out through its provider instead.
 */
export const signsInLocally = (mode: string): boolean => mode === 'local';

/**
 * Asks the gateway which sign-in it wants, retrying while it is unreachable (a restart, a reset database): the
 * app waits rather than assuming no sign-in, which would leave a stale token unchecked until a manual reload.
 */
export async function loadAuthConfig(): Promise<void> {
  keepSession();
  for (let delay = 1000; ; delay = Math.min(delay * 2, 10_000)) {
    try {
      authState.mode = (await authConfig()).authMode || 'none';
      break;
    } catch (e) {
      if (e instanceof RpcError && e.status === 404) {
        authState.mode = 'none'; // an older gateway without /api/auth/config: no sign-in
        break;
      }
      authState.unreachable = true;
      await new Promise((r) => setTimeout(r, delay));
    }
  }
  authState.unreachable = false;
  authState.loaded = true;
  check();
}

// --- the last subject that signed in, to fill the sign-in form ------------------------------------------

const LAST_SUBJECT_KEY = 'goap.lastSubject';

export function lastSubject(): string {
  try {
    return localStorage.getItem(LAST_SUBJECT_KEY) ?? '';
  } catch {
    return '';
  }
}

export function rememberSubject(subject: string): void {
  try {
    localStorage.setItem(LAST_SUBJECT_KEY, subject);
  } catch {
    // storage unavailable: the form starts empty
  }
}

// --- session keeper -----------------------------------------------------------------------------------

/** Ends the session: back to the sign-in page, which tells why. */
export function endSession(notice: string): void {
  const sub = tokenClaims(getToken())?.sub;
  if (sub) rememberSubject(sub);
  try {
    if (notice) localStorage.setItem(NOTICE_KEY, notice);
  } catch {
    // storage unavailable: the sign-in page shows no reason
  }
  setToken(null);
}

/**
 * Signing in or out starts the page afresh: every store holds the previous user's data (or none yet), and the
 * simplest way to be sure none leaks is to drop what the browser kept for them and reload on the home page.
 * A token merely replaced (refresh, project switch) is not a new session.
 */
function restartOnSessionChange(): void {
  let signedIn = !!getToken();
  onTokenChange(() => {
    const now = !!getToken();
    if (now === signedIn) return;
    signedIn = now;
    const sub = tokenClaims(getToken())?.sub;
    if (sub) rememberSubject(sub);
    forgetSessionState();
    history.replaceState(null, '', '/');
    location.reload();
  });
}

/** Seconds since the epoch. */
const now = () => Date.now() / 1000;

/**
 * When to refresh a token: a quarter of its lifetime before it expires, at most 10 minutes and at least 30
 * seconds before (a refresh needs a token that is still valid).
 */
function refreshAt(exp: number, iat: number | undefined): number {
  const lifetime = iat ? exp - iat : 3600;
  const lead = Math.max(30, Math.min(600, lifetime / 4));
  return exp - lead;
}

let timer: ReturnType<typeof setTimeout> | undefined;
let refreshing: Promise<void> | undefined;
let started = false;

/** Refreshes now (once at a time); a refusal ends the session, a network failure retries soon. */
function refreshNow(): Promise<void> {
  refreshing ??= refreshToken()
    .catch((e) => {
      if (e instanceof RpcError && e.status === 401) {
        endSession(
          /session expired/.test(e.message)
            ? 'Your session reached its maximum duration. Sign in again.'
            : /ended/.test(e.message)
              ? 'Your session was ended (signed out on another device, or the password changed). Sign in again.'
              : 'Your session expired. Sign in again.',
        );
      } else {
        // gateway unreachable or failing: try again in a minute, while the token is still valid
        clearTimeout(timer);
        timer = setTimeout(check, 60_000);
      }
    })
    .finally(() => (refreshing = undefined));
  return refreshing;
}

/** Looks at the current token: ends an expired session, refreshes one due, schedules the next look. */
function check(): void {
  clearTimeout(timer);
  const token = getToken();
  const c = tokenClaims(token);
  if (!token || !c?.exp || !signsInLocally(authState.mode)) return; // no session, a token the web cannot read or one issued elsewhere (left to the gateway)
  if (c.exp <= now() + 5) {
    endSession('Your session expired. Sign in again.');
    return;
  }
  const at = refreshAt(c.exp, c.iat);
  if (at <= now()) {
    void refreshNow();
    return;
  }
  // timers are clamped (~24.8 days) and paused while a device sleeps: look again at most every 10 minutes
  timer = setTimeout(check, Math.min((at - now()) * 1000, 600_000));
}

/**
 * Starts keeping the session (idempotent), whatever the sign-in mode: a token the gateway refuses ends the
 * session; the refresh of a token is the platform's own sign-in only (its tokens are the ones it can renew).
 */
function keepSession(): void {
  if (started) return;
  started = true;
  restartOnSessionChange();
  onTokenChange(check);
  onUnauthorized((message) => {
    endSession(
      /expired/.test(message)
        ? 'Your session expired. Sign in again.'
        : /ended/.test(message)
          ? 'Your session was ended (signed out on another device, or the password changed). Sign in again.'
          : 'Your session is no longer valid. Sign in again.',
    );
  });
  // a tab coming back (laptop woken up, tab switched to) looks at its token at once
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') check();
  });
  window.addEventListener('focus', check);
  // another tab signed in or out: follow it
  window.addEventListener('storage', (e) => {
    if (e.key === 'goap.token') setToken(e.newValue);
  });
}
