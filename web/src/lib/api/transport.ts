// Minimal client for the GOAP gateway (Connect protocol, JSON encoding).
// Each RPC is a `POST /{package.Service}/{Method}` with a JSON body
// (proto3 JSON: fields in lowerCamelCase, default values omitted).

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

import { COMMAND_HEADER, newCommandId } from '../flux/commands';
import { AT_LEAST_HEADER, CHANGE_SEQ_HEADER, atLeastFor, notePositions } from '../flux/positions';

const TOKEN_KEY = 'goap.token';

/** Base for RPC URLs: relative by default (the Vite server proxies `/goap.*`). */
export const BASE = (import.meta.env.VITE_GOAP_BASE_URL as string | undefined) ?? '';

/** Base for the Jaeger UI, used by the "Trace" links. */
export const JAEGER_URL = ((import.meta.env.VITE_GOAP_JAEGER_URL as string | undefined) ?? 'http://localhost:16686').replace(
  /\/+$/,
  '',
);

export class RpcError extends Error {
  readonly code: string;
  readonly status: number;
  /** the domain reason finer than the code (`behind`, `nothing-to-commit`), empty when none */
  readonly reason: string;

  constructor(code: string, message: string, status: number, reason = '') {
    super(message);
    this.name = 'RpcError';
    this.code = code;
    this.status = status;
    this.reason = reason;
  }
}

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

const tokenListeners = new Set<() => void>();

/** Subscribe to token changes (restarts streams). Returns the unsubscribe function. */
export function onTokenChange(fn: () => void): () => void {
  tokenListeners.add(fn);
  return () => tokenListeners.delete(fn);
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // storage unavailable (private browsing, etc.): ignore
  }
  for (const fn of tokenListeners) fn();
}

/** The claims of a JWT the web reads (payload only: the gateway verifies the signature). */
export interface TokenClaims {
  sub?: string;
  exp?: number;
  iat?: number;
  auth_time?: number;
}

/** Decodes the payload of a JWT (undefined when it is not one). */
export function tokenClaims(token: string | null): TokenClaims | undefined {
  const part = token?.split('.')[1];
  if (!part) return undefined;
  try {
    const b64 = part.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(part.length / 4) * 4, '=');
    return JSON.parse(atob(b64)) as TokenClaims;
  } catch {
    return undefined;
  }
}

const unauthorizedListeners = new Set<(message: string) => void>();

/**
 * Subscribe to the requests refused for their token (401 while one was sent): the session expired or is no longer
 * valid. Returns the unsubscribe function.
 */
export function onUnauthorized(fn: (message: string) => void): () => void {
  unauthorizedListeners.add(fn);
  return () => unauthorizedListeners.delete(fn);
}

/**
 * Reports a 401 on a request that carried `sent`: ignored when the token changed meanwhile (a refresh or a new
 * sign-in raced the request), so only the current token can end the session.
 */
export function reportUnauthorized(sent: string | null, status: number, message: string): void {
  if (status !== 401 || !sent || sent !== getToken()) return;
  for (const fn of unauthorizedListeners) fn(message);
}

/** the service whose answers carry the positions of the logs of the changes (ADR 0153) */
const GRAPH_SERVICE = 'goap.graph.v1.GraphService';
/** the header naming the domain reason of an error finer than its code (lib/rpcerr) */
const REASON_HEADER = 'Goap-Error-Reason';
/** the waits before reading again a change whose view is behind the position asked for, in ms */
export const BEHIND_RETRIES = [50, 150, 400];

export async function rpc<TReq extends object, TRes>(
  service: string,
  method: string,
  body: TReq,
  signal?: AbortSignal,
): Promise<TRes> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await rpcOnce<TReq, TRes>(service, method, body, signal);
    } catch (e) {
      // a view behind what the web already knows of the change (another replica): read it again shortly
      if (!(e instanceof RpcError && e.reason === 'behind') || attempt >= BEHIND_RETRIES.length) throw e;
      await new Promise((resolve) => setTimeout(resolve, BEHIND_RETRIES[attempt]));
      if (signal?.aborted) throw new DOMException('aborted', 'AbortError');
    }
  }
}

async function rpcOnce<TReq extends object, TRes>(service: string, method: string, body: TReq, signal?: AbortSignal): Promise<TRes> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', [COMMAND_HEADER]: newCommandId() };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const graph = service === GRAPH_SERVICE;
  const atLeast = graph ? atLeastFor(body) : undefined;
  if (atLeast) headers[AT_LEAST_HEADER] = atLeast;

  let res: Response;
  try {
    res = await fetch(`${BASE}/${service}/${method}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
      signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }

  if (graph) notePositions(res.headers.get(CHANGE_SEQ_HEADER)); // where its writes left the logs, failed or not
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }

  if (!res.ok) {
    const err = (data ?? {}) as { code?: string; message?: string };
    reportUnauthorized(token, res.status, err.message ?? '');
    throw new RpcError(
      err.code ?? (res.status === 401 ? 'unauthenticated' : 'unknown'),
      err.message ?? (text || res.statusText),
      res.status,
      res.headers.get(REASON_HEADER) ?? '',
    );
  }
  return (data ?? {}) as TRes;
}

/** Is this the answer for an object that does not exist (any more)? */
export const isNotFound = (e: unknown): boolean => e instanceof RpcError && e.code === 'not_found';

/** Human-readable error message for the UI. */
export function errorMessage(e: unknown): string {
  if (e instanceof RpcError) {
    if (e.code === 'permission_denied')
      return `Access denied: you do not have the rights required for this operation${e.message ? ` (${e.message})` : ''}.`;
    if (e.code === 'unauthenticated') return `Authentication required: sign in again${e.message ? ` (${e.message})` : ''}.`;
    return e.code ? `${e.code}: ${e.message}` : e.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}
