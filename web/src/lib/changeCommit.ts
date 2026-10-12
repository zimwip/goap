// What a change can commit and merge (ADR 0102). A change works on its own branch: a commit writes the versions of the
// modifications it accepted there and leaves the change open (it is edited and committed again); a merge takes the
// branch into the one it was forked from (rebased first) once it holds a commit, and closes the change. Only a
// modification is accepted and committed: a candidate (an impact never checked out) or a draft left as it was checked out
// is not.
import { isDraft, type Change, type ChangeImpact } from './api';

const live = (c: ChangeImpact) => !c.superseded && !c.flow && c.review !== 'rejected';

/** The impact holds a draft that modifies its node (the server's validation says so; unknown: taken as one). */
export const modifies = (c: ChangeImpact): boolean => isDraft(c.post) && c.validation?.modified !== false;

/** Whether the impact may be accepted: a modification, or the source of a merge or a split (changed by its parent). */
export const acceptable = (c: ChangeImpact): boolean => modifies(c) || !!c.via;

/** Why an impact cannot be accepted, for a disabled button's title. */
export function notAcceptableWhy(c: ChangeImpact): string | undefined {
  if (acceptable(c)) return undefined;
  return isDraft(c.post) ? 'it modifies nothing: its draft is the version it was checked out from' : 'it modifies nothing: a candidate, check it out and change it, or withdraw it';
}

/** The modifications of the main flow the next commit writes: accepted drafts that modify their node. */
export const toCommit = (cns: ChangeImpact[]): ChangeImpact[] => cns.filter((c) => live(c) && c.review === 'accepted' && modifies(c));

/** The modifications of the main flow not committed yet, whatever their review: they hold a merge. */
export const uncommitted = (cns: ChangeImpact[]): ChangeImpact[] => cns.filter((c) => live(c) && modifies(c));

/** A root change still open that committed on its own branch: it can be merged. */
export const hasCommits = (c: Change | undefined): boolean =>
  !!c && !c.parentId && !!c.resultBaselineId && (c.status === 'active' || c.status === 'draft');

export interface CommitState {
  canCommit: boolean;
  commitWhy?: string;
  canMerge: boolean;
  mergeWhy?: string;
}

/** Whether the Commit and Merge buttons of a root change are enabled, and why not. `awaiting` are the impacts awaiting
 * their review. */
export function commitState(c: Change | undefined, cns: ChangeImpact[], awaiting: ChangeImpact[]): CommitState {
  if (!c || c.status === 'applied' || c.status === 'abandoned') return { canCommit: false, commitWhy: 'The change is closed', canMerge: false, mergeWhy: 'The change is closed' };
  if (c.status === 'committed') return { canCommit: false, commitWhy: 'The merge waits for its conflicts to be resolved', canMerge: false, mergeWhy: 'Resolve the conflicts below' };
  const out: CommitState = { canCommit: true, canMerge: true };
  if (awaiting.length) {
    out.canCommit = false;
    out.commitWhy = 'Review the impacts first';
  } else if (!toCommit(cns).length) {
    out.canCommit = false;
    out.commitWhy = 'Nothing to commit: no accepted impact modifies a node';
  }
  const pending = uncommitted(cns);
  if (!hasCommits(c)) {
    out.canMerge = false;
    out.mergeWhy = 'Nothing committed on the branch of the change yet';
  } else if (pending.length) {
    out.canMerge = false;
    out.mergeWhy = `Commit or cancel the modifications first (${pending.map((n) => n.key).join(', ')})`;
  }
  return out;
}
