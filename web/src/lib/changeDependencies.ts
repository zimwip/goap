// Dependencies between changes (ADR 0108): what the change tab shows of the changes a change depends on, the changes
// depending on it and the order its sub-changes merge in, and the changes it may come to depend on. Pure, but for the
// call at the bottom; the server stays the authority (no cycle, no ancestor, no sub-change, no abandoned change).
import { graph } from './api';
import type { Change, Dependency } from './api';

const CLOSED = ['applied', 'abandoned'];

/** A change still open may have its dependencies changed. */
export function dependenciesEditable(c: Pick<Change, 'status'>): boolean {
  return ['draft', 'active'].includes(c.status ?? '');
}

/** What a dependency says of itself: satisfied, or what it waits for. */
export function dependencyNote(d: Dependency): string {
  if (d.satisfied) return 'committed where this change merges';
  return d.reason || 'not merged yet';
}

/**
 * The changes a change may come to depend on: not closed, neither itself, nor one of its ancestors or of the changes
 * under it, nor one it already depends on; by title.
 */
export function dependencyChoices(c: Change, all: Change[]): Change[] {
  const byId = new Map(all.map((x) => [x.id ?? '', x]));
  const excluded = new Set<string>(c.dependsOn ?? []);
  for (let p = c.parentId; p && !excluded.has(p); p = byId.get(p)?.parentId) excluded.add(p);
  return all
    .filter((x) => x.id && !excluded.has(x.id) && !isBelow(x.id, c.id ?? '', byId) && !CLOSED.includes(x.status ?? ''))
    .sort((a, b) => (a.title ?? '').localeCompare(b.title ?? ''));
}

/** Whether change id is the change root or one under it. */
function isBelow(id: string, root: string, byId: Map<string, Change>): boolean {
  const seen = new Set<string>();
  for (let x: string | undefined = id; x && !seen.has(x); x = byId.get(x)?.parentId) {
    if (x === root) return true;
    seen.add(x);
  }
  return false;
}

/** The dependencies with one more (kept once). */
export function withDependency(deps: string[] | undefined, id: string): string[] {
  const out = [...(deps ?? [])];
  if (id && !out.includes(id)) out.push(id);
  return out;
}

/** The dependencies without one. */
export function withoutDependency(deps: string[] | undefined, id: string): string[] {
  return (deps ?? []).filter((d) => d !== id);
}

export async function setDependencies(changeId: string, dependsOn: string[]): Promise<Change | undefined> {
  return (await graph.setChangeDependencies(changeId, dependsOn)).change;
}
