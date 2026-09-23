// État de la plateforme (GET /api/status, toutes les 15 s) et « mes
// exécutions » en cours (ListProcesses mine + flux).
import { SvelteSet } from 'svelte/reactivity';
import { engine, errorMessage, platformStatus, RpcError, type PlatformStatus, type Process } from '../api';
import { processes, ingestProcess, onLiveEvent } from './live.svelte';
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
    // Passerelle injoignable : plateforme indisponible ; point d'accès absent : état inconnu.
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

// --- mes exécutions -------------------------------------------------------------------------

export const ACTIVE = ['running', 'waiting', 'clarifying'];

/** Identifiants renvoyés par ListProcesses { mine, rootsOnly }. */
const mineIds = new SvelteSet<string>();
export const myRunsState = $state({ loading: false, error: '' });

export async function refreshMyRuns(): Promise<void> {
  myRunsState.loading = true;
  try {
    const list = (await engine.listProcesses({ mine: true, rootsOnly: true, statuses: ACTIVE })).processes ?? [];
    mineIds.clear();
    for (const p of list) {
      ingestProcess(p);
      if (p.id) mineIds.add(p.id);
    }
    myRunsState.error = '';
  } catch (e) {
    myRunsState.error = errorMessage(e);
  } finally {
    myRunsState.loading = false;
  }
}

// Processus racines démarrés par l'utilisateur et vus dans le flux.
onLiveEvent((e) => {
  const p = e.process;
  if (e.type === 'started' && p?.id && !p.parentId && (!me() || p.initiator?.subject === me())) mineIds.add(p.id);
});

/** Mes exécutions actives, les plus récentes d'abord. */
export function myActiveRuns(): Process[] {
  const out: Process[] = [];
  for (const id of mineIds) {
    const p = processes.get(id);
    if (p && ACTIVE.includes(p.status ?? '')) out.push(p);
  }
  return out.sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
}
