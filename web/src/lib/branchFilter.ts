import type { Branch } from './api';
import { MAIN_BRANCH } from './namespace';

const rank = (b: Branch) => (b.status === 'open' ? 0 : b.status === 'merged' ? 1 : 2);

/** A branch a flow owns (flow-*): drafts only, no baseline a person can browse. A change's branch (change-*) is
 * shown: it is where the versions of a change are committed before they are merged. */
export const isFlowBranch = (b: Branch): boolean => !!b.name && b.name.startsWith('flow-');

/** A change's own branch, managed by the change (committed, then merged by IntegrateChange), never by hand. */
export const isChangeBranch = (b: Branch): boolean => !!b.name && b.name.startsWith('change-');

/** The id of the change that owns a change branch (`Branch.origin` is `change:<id>`). */
export const changeOfBranch = (b: Branch): string => (b.origin?.startsWith('change:') ? b.origin.slice('change:'.length) : '');

/** The branches the explorer lists: open ones always, merged and abandoned on request; main first, then by status. */
export function shownBranches(branches: Branch[], showClosed: boolean): Branch[] {
  return branches
    .filter((b) => !isFlowBranch(b) && (showClosed || b.status === 'open'))
    .sort((a, b) => rank(a) - rank(b) || (a.name === MAIN_BRANCH ? -1 : b.name === MAIN_BRANCH ? 1 : (a.name ?? '').localeCompare(b.name ?? '')));
}
