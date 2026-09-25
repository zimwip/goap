// Chains of runs linked by relaunches: a run, the runs that restarted one of its
// steps on a flow branch (relaunchOf / fromStep / flow), and theirs in turn.
import type { Process } from './api';

/** where a run stands in its chain */
export type RunKind = 'main' | 'open' | 'adopted' | 'discarded' | 'replaced';

export type ProcessLookup = ReadonlyMap<string, Process>;

/** absolute index (in the chain) of the first step of a run: a relaunched run restarts at fromStep of its parent */
export function offsetOf(p: Process, all: ProcessLookup): number {
  let off = 0;
  const seen = new Set<string>();
  for (let cur: Process | undefined = p; cur?.relaunchOf && !seen.has(cur.id ?? ''); ) {
    seen.add(cur.id ?? '');
    off += cur.fromStep ?? 0;
    cur = all.get(cur.relaunchOf);
    if (!cur) break;
  }
  return off;
}

/** the run a chain starts from */
export function rootOf(p: Process, all: ProcessLookup): Process {
  let cur = p;
  const seen = new Set<string>();
  while (cur.relaunchOf && !seen.has(cur.id ?? '')) {
    seen.add(cur.id ?? '');
    const up = all.get(cur.relaunchOf);
    if (!up) break;
    cur = up;
  }
  return cur;
}

/** children of a run in its chain: the runs that relaunched one of its steps, oldest first */
export function relaunchesOf(p: Process, all: ProcessLookup): Process[] {
  return [...all.values()]
    .filter((c) => c.relaunchOf === p.id && c.changeId === p.changeId)
    .sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? ''));
}

/** every run of the chain of p (root first, depth first) */
export function chainOf(p: Process, all: ProcessLookup): Process[] {
  const out: Process[] = [];
  const walk = (cur: Process) => {
    if (out.includes(cur)) return;
    out.push(cur);
    for (const c of relaunchesOf(cur, all)) walk(c);
  };
  walk(rootOf(p, all));
  return out;
}

/** a run belongs to a flow chain when it relaunched, or was relaunched by, another run */
export function inChain(p: Process, all: ProcessLookup): boolean {
  return !!p.flow || !!p.relaunchOf || relaunchesOf(p, all).length > 0;
}

export function runKind(p: Process): RunKind {
  if (p.flow) {
    if (p.status === 'superseded') return 'discarded';
    if (p.status === 'completed') return 'adopted';
    return 'open';
  }
  return p.status === 'superseded' ? 'replaced' : 'main';
}
