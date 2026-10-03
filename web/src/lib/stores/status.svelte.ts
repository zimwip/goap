// Platform status (GET /api/status, every 15 s) and "my runs" in progress (from the process store).
import { errorMessage, platformStatus, RpcError, type PlatformStatus, type Process } from '../api';
import { live, processes } from './live.svelte';
import { me } from './session.svelte';

export const health = $state({
  status: 'unknown' as 'ok' | 'degraded' | 'down' | 'unknown' | string,
  data: undefined as PlatformStatus | undefined,
  error: '',
  checkedAt: '',
});

export async function refreshHealth(): Promise<void> {
  try {
    const s = await platformStatus();
    health.data = s;
    health.status = s.status ?? 'unknown';
    health.error = '';
  } catch (e) {
    health.data = undefined;
    // Gateway unreachable: platform unavailable; endpoint missing: status unknown.
    health.status = e instanceof RpcError && e.code === 'unimplemented' ? 'unknown' : 'down';
    health.error = errorMessage(e);
  } finally {
    health.checkedAt = new Date().toISOString();
  }
}

export function startHealth(): () => void {
  void refreshHealth();
  const t = setInterval(() => void refreshHealth(), 15_000);
  return () => clearInterval(t);
}

// --- my runs -------------------------------------------------------------------------

export const ACTIVE = ['running', 'waiting', 'clarifying'];

/** Loading state of the process list the platform event stream keeps (see `stores/live.svelte`). */
export const myRunsState = {
  get loading() {
    return live.processesLoading;
  },
  get error() {
    return live.processesError;
  },
};

/** My active root runs, most recent first: derived from the known processes, which the stream keeps current. */
export function myActiveRuns(): Process[] {
  const subject = me();
  const out: Process[] = [];
  for (const p of processes.values()) {
    if (!p.parentId && ACTIVE.includes(p.status ?? '') && (!subject || p.initiator?.subject === subject)) out.push(p);
  }
  return out.sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
}
