import { BASE, RpcError, getToken, reportUnauthorized } from './transport';

// --- platform status (gateway, outside RPC) -------------------------------

export interface ServiceStatus {
  name?: string;
  status?: 'up' | 'down' | string;
  latencyMs?: number;
  error?: string;
}

export interface PlatformStatus {
  status?: 'ok' | 'degraded' | 'down' | string;
  services?: ServiceStatus[];
  time?: string;
}

/** `GET /api/status` served by the gateway. */
export async function platformStatus(signal?: AbortSignal): Promise<PlatformStatus> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/status`, { headers, signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Gateway unreachable: ${String(e)}`, 0);
  }
  const text = await res.text();
  let data: PlatformStatus | undefined;
  try {
    data = text ? (JSON.parse(text) as PlatformStatus) : undefined;
  } catch {
    data = undefined;
  }
  reportUnauthorized(token, res.status, '');
  // 503 with a status body: platform unavailable, but a usable response.
  if (data?.status) return data;
  throw new RpcError(res.status === 404 ? 'unimplemented' : 'unknown', text || res.statusText, res.status);
}
