// What a change can commit and merge (ADR 0102). A change works on its own branch: a commit writes the accepted
// modifications there (and reverts the nodes withdrawn since their commit) and leaves the change open; the merge takes the
// branch into the one it was forked from once everything in it is settled, and closes the change. The server judges both
// (graph.getCommitState): this module only words its answer and says why an impact cannot be accepted.
import { isDraft, type ChangeImpact, type CommitState } from './api';

/** The server's validation says the draft of the impact modifies nothing (ADR 0102). The JSON of a validation omits
 * `modified` when it is false, so a validation without it is one of an unmodified draft; no validation: unknown. */
export const unmodified = (c: ChangeImpact): boolean => !!c.validation && c.validation.modified !== true;

/** The impact holds a draft that modifies its node (the server's validation says so; unknown: taken as one). */
export const modifies = (c: ChangeImpact): boolean => isDraft(c.post) && !unmodified(c);

/** The impact is committed: its post is the version its draft was written as (ADR 0079), no longer a draft. */
export const committed = (c: ChangeImpact): boolean => !!c.committed?.id || (!!c.post && !isDraft(c.post));

/** Why an impact cannot be accepted (it modifies nothing: a candidate, a draft left as it was checked out); undefined when
 * it may be, or when it is committed (its acceptance was judged on its draft, ADR 0152). */
export function notAcceptableWhy(c: ChangeImpact): string | undefined {
  if (modifies(c) || committed(c)) return undefined;
  return isDraft(c.post) ? 'it modifies nothing: its draft is the version it was checked out from' : 'it modifies nothing: a candidate, check it out and change it, or withdraw it';
}

export interface CommitButtons {
  canCommit: boolean;
  /** why Commit is off, else what it writes */
  commitWhy?: string;
  commitWhat?: string;
  canMerge: boolean;
  mergeWhy?: string;
}

/** The Commit and Merge buttons of a change from the server's state; closed: both off. */
export function commitButtons(st: CommitState | undefined, closed: boolean): CommitButtons {
  if (closed || !st) return { canCommit: false, commitWhy: closed ? 'The change is closed' : undefined, canMerge: false, mergeWhy: closed ? 'The change is closed' : undefined };
  const out: CommitButtons = { canCommit: !!st.canCommit, canMerge: !!st.canMerge };
  if (!out.canCommit) out.commitWhy = 'Nothing to commit: no accepted impact modifies a node, nothing withdrawn to revert';
  else out.commitWhat = [...(st.toCommit ?? []), ...(st.reverts ? [`${st.reverts} withdrawn node(s) reverted`] : [])].join(', ');
  if (!out.canMerge) out.mergeWhy = st.unsettled?.length ? `Settle first: ${st.unsettled.join('; ')}` : 'Nothing committed on the branch of the change yet';
  return out;
}
