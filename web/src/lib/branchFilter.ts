import type { Commit, Branch } from './api';
import { MAIN_BRANCH } from './namespace';

/** A branch a flow owns (flow-*): drafts only, no commit a person can browse. A change's branch (change-*) is
 * shown: it is where the versions of a change are committed before they are merged. */
export const isFlowBranch = (b: Branch): boolean => !!b.name && b.name.startsWith('flow-');

/** A change's own branch, managed by the change (committed, then merged by IntegrateChange), never by hand. */
export const isChangeBranch = (b: Branch): boolean => !!b.name && b.name.startsWith('change-');

/** The id of the change that owns a change branch (`Branch.origin` is `change:<id>`). */
export const changeOfBranch = (b: Branch): string => (b.origin?.startsWith('change:') ? b.origin.slice('change:'.length) : '');

/**
 * The logical time of every commit: 1 + the latest of its parents (the commit it follows, and the head merged into
 * it), 0 for the empty state. A change starts from the result of another one, so this is the order of the history,
 * whatever the clocks say (a Lamport clock over the commit graph).
 */
export function logicalTimes(commits: Iterable<Commit>): Map<string, number> {
  const byId = new Map<string, Commit>();
  for (const b of commits) if (b.id) byId.set(b.id, b);
  const time = new Map<string, number>();
  for (const root of byId.keys()) {
    const stack = [root];
    while (stack.length) {
      const id = stack[stack.length - 1];
      if (time.has(id)) {
        stack.pop();
        continue;
      }
      const b = byId.get(id);
      const parents = [b?.parentId, b?.mergedFrom].filter((p): p is string => !!p && byId.has(p));
      const open = parents.filter((p) => !time.has(p));
      if (open.length && !stack.includes(open[0])) {
        stack.push(open[0]); // a parent first (a cycle is cut by the stack check: it would be corrupt data)
        continue;
      }
      time.set(id, parents.length ? 1 + Math.max(...parents.map((p) => time.get(p) ?? 0)) : 1);
      stack.pop();
    }
  }
  return time;
}

/** When a branch was opened in the history: the logical time of the commit it forked from (0: the empty state). */
const openedAt = (b: Branch, times: Map<string, number>): number => (b.forkCommit ? (times.get(b.forkCommit) ?? 0) : 0);

/**
 * The branches the explorer lists: open ones always, merged and abandoned on request; main first, then the latest in
 * the history first (the logical time of its fork point, then its creation date).
 */
export function shownBranches(branches: Branch[], showClosed: boolean, commits: Iterable<Commit> = []): Branch[] {
  const times = logicalTimes(commits);
  return branches
    .filter((b) => !isFlowBranch(b) && (showClosed || b.status === 'open'))
    .sort((a, b) =>
      a.name === MAIN_BRANCH ? -1 : b.name === MAIN_BRANCH ? 1
        : openedAt(b, times) - openedAt(a, times) || (b.createdAt ?? '').localeCompare(a.createdAt ?? '') || (a.name ?? '').localeCompare(b.name ?? ''),
    );
}
