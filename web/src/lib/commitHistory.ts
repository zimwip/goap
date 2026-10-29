// The history of a branch as lanes (CommitBranchGraph): its own commits in its lane, and, for each change that
// committed on it, the change's own branch as a lane of its own with its commits (ADR 0175), from the commit the
// change started on to the commit the merge joined. Two changes started from the same commit and applied one after the
// other read as two lanes forking from the same dot, the second joining by a merge. The branches forked from the branch
// shown and still open have a lane too (ADR 0102): their commits, else, a branch that committed nothing yet, its open
// head, drawn as a transparent dot on the lane leaving the commit it forked from.
import { formatDate, shortId, type Commit, type Branch } from './api';
import type { ChangeStats } from './api/types/graph';
import type { LaneEdge, LaneEntry } from './components/HistoryGraph.svelte';
import { MAIN_BRANCH } from './namespace';

const branchOf = (b: Commit) => b.branch || MAIN_BRANCH;
/** the branch a change owns (change-<id>): where its versions are committed before they are merged */
const isChangeLane = (name: string) => name.startsWith('change-');

/** the commit of each change, on the branch of the change: what the merge or fast-forward onto a branch joined */
function commitsOf(commits: Commit[], branch: string): Map<string, Commit> {
  const out = new Map<string, Commit>();
  for (const b of commits) {
    if (!b.changeId || branchOf(b) === branch || !isChangeLane(branchOf(b))) continue;
    const seen = out.get(b.changeId);
    if (!seen || (b.createdAt ?? '') > (seen.createdAt ?? '')) out.set(b.changeId, b); // the head of the change's branch
  }
  return out;
}

/** A branch as the views name it: a change's branch by the change's short id (`change-<8 chars>`), any other whole. */
export const branchLabel = (name: string) => (isChangeLane(name) ? name.slice(0, 'change-'.length + 8) : name);

/** Whether a row of the history matches the text typed in its filter: its title, change, branch, kind or date. */
export function matchesCommit(b: Commit, query: string, title = ''): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const text = [title, b.name, b.id, b.changeId, b.branch || MAIN_BRANCH, typeOf(b), b.createdAt, b.createdAt ? formatDate(b.createdAt) : '']
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
  return q.split(/\s+/).every((w) => text.includes(w));
}

/** The counters of a change, short (`+2 ~5 −1`, nodes created, modified, deleted), with what they count; none: nothing. */
export function countsOf(s: ChangeStats | undefined): { text: string; title: string } | undefined {
  const parts: [number | undefined, string, string][] = [
    [s?.created, '+', 'created'],
    [s?.modified, '~', 'modified'],
    [s?.deleted, '−', 'deleted'],
    [s?.restored, '↺', 'restored'],
  ];
  const shown = parts.filter(([n]) => !!n);
  if (!shown.length) return undefined;
  return {
    text: shown.map(([n, sign]) => `${sign}${n}`).join(' '),
    title: `nodes the change committed: ${shown.map(([n, , what]) => `${n} ${what}`).join(', ')}`,
  };
}

export type CommitKind = 'commit' | 'fast-forward' | 'merge' | 'rebase' | 'open' | 'snapshot';

/** The kind of the row standing for an open branch with no commit of its own (not a commit: a transparent dot). */
export const OPEN_KIND = 'open';
const OPEN_PREFIX = 'open:';
/** Whether a row id is the open head of a branch without commit, not a commit. */
export const isOpenHead = (id: string) => id.startsWith(OPEN_PREFIX);

/** How a commit came to be: the kind the server records, else read from its shape (a commit sits on a change's lane). */
export function typeOf(b: Commit): CommitKind {
  if (b.kind === 'commit' || b.kind === 'fast-forward' || b.kind === 'merge' || b.kind === 'rebase' || b.kind === OPEN_KIND) return b.kind;
  if (b.mergedFrom) return 'merge';
  if (isChangeLane(branchOf(b))) return 'commit';
  return b.changeId ? 'fast-forward' : 'snapshot';
}

const nameOf = (b: Commit | undefined, id = '') => b?.name || shortId(b?.id || id);

/** What a row says about how its commit came to be: kind badge, rebase note, merge note (ADR 0101 §4). */
export interface HistoryBadges {
  kind: CommitKind;
  /** "rebased onto <parent>, was <rebasedFrom>" */
  rebased?: string;
  /** "merged change <id> from <branch>" */
  merged?: string;
}

export function badgesOf(b: Commit, commits: Commit[]): HistoryBadges {
  const byId = new Map(commits.map((x) => [x.id ?? '', x]));
  const out: HistoryBadges = { kind: typeOf(b) };
  // the rebase of a committed branch (ADR 0102) names the head it moved onto as its second parent
  const onto = out.kind === 'rebase' ? b.mergedFrom : b.parentId;
  if (b.rebasedFrom) out.rebased = `rebased onto ${nameOf(byId.get(onto ?? ''), onto)}, was ${nameOf(byId.get(b.rebasedFrom), b.rebasedFrom)}`;
  if (b.mergedFrom && out.kind !== 'rebase') {
    const src = byId.get(b.mergedFrom);
    const change = src?.changeId || b.changeId;
    out.merged = `merged ${change ? `change ${change.slice(0, 8)}` : 'branch'}${src ? ` from ${branchOf(src)}` : ''}`;
  }
  return out;
}

/**
 * Rows of the history of `branch`, newest first. `forkCommit` anchors a branch that has no commit of its own yet.
 * `labelOf` names the edge a commit descends through (the change that produced it).
 */
export function historyEntries(
  commits: Commit[],
  branch: string,
  forkCommit = '',
  labelOf: (b: Commit) => string | undefined = () => undefined,
  branches: Branch[] = [],
  /** false: the branch's own commits only, without the lanes of the changes and of the open branches */
  lanes = true,
): LaneEntry<Commit>[] {
  const byId = new Map(commits.map((b) => [b.id ?? '', b]));
  const own = commits.filter((b) => branchOf(b) === branch);
  const committed = new Set(own.map((b) => b.changeId).filter((c): c is string => !!c));
  const byChange = commitsOf(commits, branch);
  // the lanes of the changes that committed here, every commit of their branch (a change branch seen from itself has
  // none: its commits are its own)
  const heads = new Set([...byChange.entries()].filter(([change]) => committed.has(change)).map(([, b]) => b.id));
  const changeLanes = new Set(lanes ? commits.filter((b) => heads.has(b.id)).map(branchOf) : []);
  const side = commits.filter((b) => branchOf(b) !== branch && changeLanes.has(branchOf(b)));
  const present = new Set([...own, ...side].map((b) => b.id));

  const edges = (b: Commit, joined: Commit | undefined): LaneEdge[] => {
    const seen = new Set<string>();
    const kind = typeOf(b);
    const tag = (what: string) => [what, labelOf(b)].filter(Boolean).join(' · ') || undefined;
    const commitParent = kind === 'commit' && branchOf(byId.get(b.parentId ?? '') ?? {}) === branchOf(b) ? 'commit' : 'fork';
    return [
      // a commit forks from the commit it started on (its parent, the head it was rebased onto when it was), or follows
      // the previous commit of its branch
      ...(b.parentId ? [{ id: b.parentId, label: tag(kind === 'commit' ? commitParent : kind === 'rebase' ? 'commit' : kind === 'snapshot' ? '' : kind) }] : []),
      // a merge names what it joined; a fast-forward does not, the commit of its change is what it joined; a rebase names
      // the head it moved onto
      ...(b.mergedFrom && byId.has(b.mergedFrom)
        ? [{ id: b.mergedFrom, label: tag(kind === 'rebase' ? 'rebased onto' : 'merge') }]
        : joined
          ? [{ id: joined.id ?? '', label: tag('fast-forward') }]
          : []),
    ].filter((p) => (seen.has(p.id) ? false : (seen.add(p.id), true)));
  };

  const rows: LaneEntry<Commit>[] = [
    ...own.map((b) => ({ id: b.id ?? '', parents: edges(b, b.changeId && !b.mergedFrom ? byChange.get(b.changeId) : undefined), lane: branch, item: b })),
    ...side.map((b) => ({ id: b.id ?? '', parents: edges(b, undefined), lane: branchOf(b), item: b })),
  ];
  // the open branches forked from this one (ADR 0102): their own commits, else their open head as a transparent dot
  for (const br of lanes ? branches : []) {
    const name = br.name ?? '';
    if (!name || name === branch || (br.parent || MAIN_BRANCH) !== branch || br.status !== 'open' || name.startsWith('flow-')) continue;
    const mine = commits.filter((b) => branchOf(b) === name && !present.has(b.id));
    for (const b of mine) {
      rows.push({ id: b.id ?? '', parents: edges(b, undefined), lane: name, item: b });
      present.add(b.id);
    }
    if (mine.length || commits.some((b) => branchOf(b) === name)) continue;
    const id = OPEN_PREFIX + name;
    const changeId = br.origin?.startsWith('change:') ? br.origin.slice('change:'.length) : undefined;
    rows.push({
      id,
      parents: br.forkCommit ? [{ id: br.forkCommit, label: 'fork' }] : [],
      lane: name,
      pending: true,
      item: { id, name, branch: name, kind: OPEN_KIND, changeId, createdAt: br.createdAt },
    });
    present.add(id);
  }
  // the commits an edge reaches that no row shows: the fork point of the branch, a named branch's merge source
  const ghosts = new Set<string>();
  if (forkCommit && !present.has(forkCommit)) ghosts.add(forkCommit);
  // (without lanes, only the commits of other branches the branch's own commits descend from: not a change's commit)
  for (const r of rows) for (const p of r.parents) if (!present.has(p.id) && (lanes || !isChangeLane(branchOf(byId.get(p.id) ?? {})))) ghosts.add(p.id);
  for (const id of ghosts) {
    const g = byId.get(id);
    if (g) rows.push({ id, parents: [], lane: branchOf(g), item: g });
  }
  return rows.sort((a, b) => (b.item.createdAt ?? '').localeCompare(a.item.createdAt ?? ''));
}
