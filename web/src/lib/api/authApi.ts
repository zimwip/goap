import { BASE, RpcError, getToken, reportUnauthorized, setToken } from './transport';
import type { Session } from './types/engine';

/** The caller as the platform sees it: token principal completed by its User node (GET /api/whoami). */
export async function whoAmI(signal?: AbortSignal): Promise<Session> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const res = await fetch(`${BASE}/api/whoami`, { headers, signal });
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError('unauthenticated', message, res.status);
  }
  return (await res.json()) as Session;
}

/**
 * Switches the active project (ADR 0039): reissues the token with the same subject/org/roles, pointed at
 * a different project (POST /auth/dev-token/project), and stores it — every call from here on carries it.
 * Requires a token (the dev-token / hs256 auth flow; a deployment without one has no project to switch).
 */
export async function switchProject(project: string): Promise<void> {
  const token = getToken();
  if (!token) throw new RpcError('unauthenticated', 'no active token', 401);
  const res = await fetch(`${BASE}/auth/dev-token/project`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ project }),
  });
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', message, res.status);
  }
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  setToken(data.token);
}

/** The message of an HTTP error response: the `message` of a JSON body (echo, Connect), else its text. */
async function errorText(res: Response): Promise<string> {
  const text = await res.text().catch(() => '');
  try {
    const m = (JSON.parse(text) as { message?: string }).message;
    if (m) return m;
  } catch {
    // not JSON
  }
  return text || res.statusText;
}

/**
 * Reissues the current token with a fresh expiry (POST /auth/refresh, local sign-in): keeps an active session
 * going. Refused (401) when the token expired, is invalid, or the session reached its maximum age.
 */
export async function refreshToken(): Promise<void> {
  const token = getToken();
  if (!token) throw new RpcError('unauthenticated', 'no active token', 401);
  let res: Response;
  try {
    res = await fetch(`${BASE}/auth/refresh`, { method: 'POST', headers: { Authorization: `Bearer ${token}` } });
  } catch (e) {
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  if (!res.ok) {
    const message = await errorText(res);
    reportUnauthorized(token, res.status, message);
    throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', message, res.status);
  }
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  // a sign-out or another refresh meanwhile wins
  if (getToken() === token) setToken(data.token);
}

/** Which sign-in UI to show (GET /api/auth/config, unauthenticated — ADR 0040): the mode's name ('none', 'hs256', 'local') and
 *  whether the platform signs its users in itself (`signsIn`, the mode keeps sessions). */
export async function authConfig(signal?: AbortSignal): Promise<{ authMode: string; signsIn?: boolean }> {
  const res = await fetch(`${BASE}/api/auth/config`, { signal });
  if (!res.ok) throw new RpcError('failed', res.statusText, res.status);
  return (await res.json()) as { authMode: string; signsIn?: boolean };
}

async function authToken(path: string, subject: string, password: string): Promise<void> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subject, password }),
  });
  if (!res.ok) throw new RpcError(res.status === 401 ? 'unauthenticated' : 'failed', await errorText(res), res.status);
  const data = (await res.json()) as { token?: string };
  if (!data.token) throw new RpcError('failed', 'no token returned', res.status);
  setToken(data.token);
}

/** Creates a local account and signs in (POST /auth/register, ADR 0040: no external identity provider). */
export const register = (subject: string, password: string): Promise<void> => authToken('/auth/register', subject, password);

/** Signs in with a local account (POST /auth/login). */
export const login = (subject: string, password: string): Promise<void> => authToken('/auth/login', subject, password);

/**
 * Signs out (POST /auth/logout, ADR 0045): the gateway ends the session of the token, so its tokens are refused
 * from now on — every session of the user with `everywhere` (all their devices) — and the local token is cleared.
 */
export async function logout(everywhere = false): Promise<void> {
  const token = getToken();
  try {
    await fetch(`${BASE}/auth/logout`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify({ everywhere }),
    });
  } finally {
    setToken(null);
  }
}
