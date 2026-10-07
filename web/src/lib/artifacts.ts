// The Artifacts pane of a change: the artifact items of a scope, the item-level decisions shown on the artifact
// they concern, and the item kinds that have no pane of their own (debug view).
import type { ChangeItem } from './api';

/** Item kinds with a dedicated pane or tab (Reviews, Risks & actions, Verification, Derogations, Decisions, Changes, Audit). */
export const DEDICATED_KINDS: readonly string[] = [
  'artifact', 'decision', 'review', 'risk', 'action', 'waiver', 'verification', 'derogation', 'decision_point', 'transition', 'flow',
];

export const artifactsOf = (items: ChangeItem[]): ChangeItem[] => items.filter((i) => i.kind === 'artifact');

/** The item-level decisions (`kind: decision`) pointing at each item id, latest first. */
export function decisionsByItem(items: ChangeItem[]): Map<string, ChangeItem[]> {
  const out = new Map<string, ChangeItem[]>();
  for (const i of items) {
    const target = i.kind === 'decision' ? i.decision?.item : undefined;
    if (!target) continue;
    out.set(target, [...(out.get(target) ?? []), i]);
  }
  for (const [k, list] of out) {
    // stable: the log order breaks ties, the latest entry first
    out.set(k, list.map((d, n) => ({ d, n })).sort((a, b) => (b.d.createdAt ?? '').localeCompare(a.d.createdAt ?? '') || b.n - a.n).map((x) => x.d));
  }
  return out;
}

/** Items of a kind no pane shows (merge, signal...): the raw debug list. */
export const rawItems = (items: ChangeItem[]): ChangeItem[] => items.filter((i) => !DEDICATED_KINDS.includes(i.kind ?? ''));
