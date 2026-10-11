// The history of a branch as lanes (BaselineBranchGraph): its own baselines in its lane, and, for each change that
// landed on it, the change's own branch as a lane of its own, from the baseline the change started on to the commit
// the merge joined. Two changes started from the same baseline and applied one after the other read as two lanes
// forking from the same dot, the second joining by a merge.
import { shortId, type Baseline } from './api';
import type { LaneEdge, LaneEntry } from './components/HistoryGraph.svelte';
import { MAIN_BRANCH } from './namespace';

const branchOf = (b: Baseline) => b.branch || MAIN_BRANCH;
/** the branch a change owns (change-<id>): where its versions are committed before they are merged */
const isChangeLane = (name: string) => name.startsWith('change-');

/** the commit of each change, on the branch of the change: what the merge or fast-forward onto a branch joined */
function commitsOf(baselines: Baseline[], branch: string): Map<string, Baseline> {
  const out = new Map<string, Baseline>();
  for (const b of baselines) {
    if (!b.changeId || branchOf(b) === branch || !isChangeLane(branchOf(b))) continue;
    const seen = out.get(b.changeId);
    if (!seen || (b.createdAt ?? '') > (seen.createdAt ?? '')) out.set(b.changeId, b); // the head of the change's branch
  }
  return out;
}

export type BaselineKind = 'commit' | 'fast-forward' | 'merge' | 'snapshot';

/** How a baseline came to be: the kind the server records, else read from its shape (a commit sits on a change's lane). */
export function kindOf(b: Baseline): BaselineKind {
  if (b.kind === 'commit' || b.kind === 'fast-forward' || b.kind === 'merge') return b.kind;
  if (b.mergedFrom) return 'merge';
  if (isChangeLane(branchOf(b))) return 'commit';
  return b.changeId ? 'fast-forward' : 'snapshot';
}

const nameOf = (b: Baseline | undefined, id = '') => b?.name || shortId(b?.id || id);

/** What a row says about how its baseline came to be: kind badge, rebase note, merge note (ADR 0101 §4). */
export interface HistoryBadges {
  kind: BaselineKind;
  /** "rebased onto <parent>, was <rebasedFrom>" */
  rebased?: string;
  /** "merged change <id> from <branch>" */
  merged?: string;
}

export function badgesOf(b: Baseline, baselines: Baseline[]): HistoryBadges {
  const byId = new Map(baselines.map((x) => [x.id ?? '', x]));
  const out: HistoryBadges = { kind: kindOf(b) };
  if (b.rebasedFrom) out.rebased = `rebased onto ${nameOf(byId.get(b.parentId ?? ''), b.parentId)}, was ${nameOf(byId.get(b.rebasedFrom), b.rebasedFrom)}`;
  if (b.mergedFrom) {
    const src = byId.get(b.mergedFrom);
    const change = src?.changeId || b.changeId;
    out.merged = `merged ${change ? `change ${change.slice(0, 8)}` : 'branch'}${src ? ` from ${branchOf(src)}` : ''}`;
  }
  return out;
}

/**
 * Rows of the history of `branch`, newest first. `forkBaseline` anchors a branch that has no baseline of its own yet.
 * `labelOf` names the edge a baseline descends through (the change that produced it).
 */
export function historyEntries(baselines: Baseline[], branch: string, forkBaseline = '', labelOf: (b: Baseline) => string | undefined = () => undefined): LaneEntry<Baseline>[] {
  const byId = new Map(baselines.map((b) => [b.id ?? '', b]));
  const own = baselines.filter((b) => branchOf(b) === branch);
  const landed = new Set(own.map((b) => b.changeId).filter((c): c is string => !!c));
  const commits = commitsOf(baselines, branch);
  // the lanes of the changes that landed here (a change branch seen from itself has none: its commit is its own)
  const side = [...commits.entries()].filter(([change]) => landed.has(change)).map(([, b]) => b);
  const present = new Set([...own, ...side].map((b) => b.id));

  const edges = (b: Baseline, joined: Baseline | undefined): LaneEdge[] => {
    const seen = new Set<string>();
    const kind = kindOf(b);
    const tag = (what: string) => [what, labelOf(b)].filter(Boolean).join(' · ') || undefined;
    return [
      // a commit forks from the baseline it started on (its parent, the head it was rebased onto when it was)
      ...(b.parentId ? [{ id: b.parentId, label: tag(kind === 'commit' ? 'fork' : kind === 'snapshot' ? '' : kind) }] : []),
      // a merge names what it joined; a fast-forward does not, the commit of its change is what it joined
      ...(b.mergedFrom && byId.has(b.mergedFrom) ? [{ id: b.mergedFrom, label: tag('merge') }] : joined ? [{ id: joined.id ?? '', label: tag('fast-forward') }] : []),
      // a rebase leaves the baseline the change was forked from before: a dashed edge next to the solid fork edge
      ...(b.rebasedFrom && byId.has(b.rebasedFrom) ? [{ id: b.rebasedFrom, label: tag('rebased from'), dashed: true }] : []),
    ].filter((p) => (seen.has(p.id) ? false : (seen.add(p.id), true)));
  };

  const rows: LaneEntry<Baseline>[] = [
    ...own.map((b) => ({ id: b.id ?? '', parents: edges(b, b.changeId && !b.mergedFrom ? commits.get(b.changeId) : undefined), lane: branch, item: b })),
    ...side.map((b) => ({ id: b.id ?? '', parents: edges(b, undefined), lane: branchOf(b), ephemeral: true, item: b })),
  ];
  // the baselines an edge reaches that no row shows: the fork point of the branch, a named branch's merge source
  const ghosts = new Set<string>();
  if (forkBaseline && !present.has(forkBaseline)) ghosts.add(forkBaseline);
  for (const r of rows) for (const p of r.parents) if (!present.has(p.id)) ghosts.add(p.id);
  for (const id of ghosts) {
    const g = byId.get(id);
    if (g) rows.push({ id, parents: [], lane: branchOf(g), item: g });
  }
  return rows.sort((a, b) => (b.item.createdAt ?? '').localeCompare(a.item.createdAt ?? ''));
}
