// Adopting or discarding a flow branch from anywhere in the UI.
import { engine, graph, type Flow, type Process } from './api';
import { processes, refreshProcesses } from './stores/live.svelte';

/** the run working on a flow branch */
export function processOfFlow(flow: Flow): Process | undefined {
  return [...processes.values()].find((p) => p.flow === flow.id);
}

/** why a flow cannot be adopted now (empty: it can) */
export function adoptBlockedReason(flow: Flow, p: Process | undefined): string {
  if (flow.status !== 'open') return `The flow is ${flow.status}.`;
  if (flow.competesWith?.length) {
    return `It competes with the adopted flow ${flow.competesWith[0].slice(0, 8)} (it replaces the same items): discard it or relaunch the step.`;
  }
  if (!p) return 'Its run is not loaded yet.';
  if (p.pending?.kind !== 'flow' || p.status !== 'waiting') {
    return 'Its run has not reached the goal yet: wait for it, then adopt.';
  }
  return '';
}

/**
 * Adopts or discards an open flow. Through the run when it is known (the run states stay right),
 * else straight on the graph.
 */
export async function decideFlow(flow: Flow, changeId: string, adopt: boolean, comment = ''): Promise<void> {
  let p = processOfFlow(flow);
  if (!p) {
    await refreshProcesses();
    p = processOfFlow(flow);
  }
  if (p?.id) {
    await engine.decideFlow(p.id, adopt, comment);
    return;
  }
  if (adopt) await graph.adoptFlow(changeId, flow.id ?? '');
  else await graph.discardFlow(changeId, flow.id ?? '');
}

/** number of items a flow marks stale */
export const staleCount = (f: Flow): number => f.stale?.length ?? 0;
