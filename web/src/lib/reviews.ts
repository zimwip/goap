// The review object of a change (ADR 0080): a review built up while open (a global comment, one entry per change
// impact with its own comment and outcome), then submitted. It is an item of type `review`, each item a delta of the
// review of its data.key (ADR 0105 addendum, the server's domain.ReviewFold); this module folds the items, derives the
// verdicts a submission decides, and holds the pure rules
// the panel shows: which impacts await a review, when a review may be submitted, the comment each impact keeps.
import type { ChangeImpact, ChangeItem, NodeRef, ReviewEntry, ReviewRecord } from './api';
import { notAcceptableWhy } from './changeCommit';

export const REVIEW_KIND = 'review';
export type Outcome = 'accept' | 'reject';
export const OUTCOMES: { id: Outcome; label: string }[] = [
  { id: 'accept', label: 'Accept' },
  { id: 'reject', label: 'Reject' },
];

const str = (v: unknown) => (typeof v === 'string' ? v.trim() : '');

/** A review as the deltas of its key fold it, with the time each outcome was set and who submitted it. */
export interface FoldedReview extends ReviewRecord {
  /** when each entry's outcome was set (the time of its delta: the time of the verdict), by impact */
  decidedAt: Record<string, string>;
  /** who decided an entry when it is not the submitter (a merge carrying a sub-change's verdict), by impact */
  decidedBy: Record<string, string>;
  submittedBy?: string;
  execution?: string;
}

/** The reviews of a change folded from the deltas of their items (ADR 0105 addendum, the server's domain.ReviewFold):
 * each item is the gesture of the reviewer (status opening, submitting or discarding; comment; remove; entries added or
 * their comment and outcome set). A submitted or discarded review is final (a later delta is ignored), the first delta
 * of a key opens it (open, or submitted at once); a rejected or superseded item is not a delta. */
export class ReviewFold {
  private at = new Map<string, number>();
  readonly list: FoldedReview[] = [];

  /** Folds an item; answers the review after it and whether the item submitted it. */
  apply(it: ChangeItem): { review?: FoldedReview; submitted: boolean } {
    if (it.type !== REVIEW_KIND || it.status === 'rejected' || it.status === 'superseded') return { submitted: false };
    const d = (it.data ?? {}) as Record<string, unknown>;
    const key = str(d.key);
    const status = str(d.status);
    if (!key || (status && !['open', 'submitted', 'discarded'].includes(status))) return { submitted: false };
    const i = this.at.get(key);
    let r: FoldedReview;
    if (i !== undefined) {
      r = this.list[i];
      if (r.status !== 'open') return { review: r, submitted: false };
      r = { ...r, entries: [...(r.entries ?? [])], decidedAt: { ...r.decidedAt }, decidedBy: { ...r.decidedBy } };
    } else {
      if (status !== 'open' && status !== 'submitted') return { submitted: false };
      r = { key, flow: it.flow ?? '', comment: '', status: 'open', entries: [], by: str(d.by) || it.producedBy || '', versions: 0, decidedAt: {}, decidedBy: {} };
    }
    if (typeof d.comment === 'string') r.comment = d.comment.trim();
    const remove = Array.isArray(d.remove) ? d.remove.map(str) : [];
    r.entries = (r.entries ?? []).filter((e) => !remove.includes(e.changeImpactId));
    // what most entries set alike is the delta's defaults, an entry its impact alone or what differs (null: unset)
    const defaults = d.defaults && typeof d.defaults === 'object' ? (d.defaults as Record<string, unknown>) : {};
    for (const x of Array.isArray(d.entries) ? d.entries : []) {
      const own = typeof x === 'string' ? { impact: x } : x && typeof x === 'object' ? (x as Record<string, unknown>) : undefined;
      if (!own) continue;
      const m: Record<string, unknown> = { ...defaults, ...own };
      const imp = str(m.impact);
      const j = r.entries.findIndex((y) => y.changeImpactId === imp);
      const e: ReviewEntry = j < 0 ? { changeImpactId: imp, comment: '', outcome: '' } : { ...r.entries[j] };
      if (j < 0) r.entries.push(e);
      else r.entries[j] = e;
      if (typeof m.comment === 'string') e.comment = m.comment.trim();
      if (typeof m.outcome === 'string') {
        e.outcome = m.outcome.trim();
        r.decidedAt[imp] = it.createdAt ?? '';
      }
      if (str(m.by)) r.decidedBy[imp] = str(m.by);
    }
    if (status) r.status = status;
    if (r.status === 'submitted') {
      r.submittedAt = it.createdAt ?? '';
      r.submittedBy = it.producedBy ?? '';
      r.execution = it.execution ?? '';
    }
    r.item = it.id;
    r.versions = (r.versions ?? 0) + 1;
    if (i !== undefined) this.list[i] = r;
    else {
      this.at.set(key, this.list.length);
      this.list.push(r);
    }
    return { review: r, submitted: r.status === 'submitted' };
  }
}

/** The reviews of a change: the fold of the deltas of each key, in the order the keys appeared. */
export function foldReviews(items: ChangeItem[]): ReviewRecord[] {
  const f = new ReviewFold();
  for (const it of items) f.apply(it);
  return f.list;
}

/** A verdict a submitted review decides: never stored, derived from the item that submitted it (ADR 0105 addendum), as
 * the reviewed event of the server's ReviewState.Verdicts. */
export interface DerivedVerdict {
  id: string;
  changeId?: string;
  seq?: number;
  impactId: string;
  op: 'reviewed';
  flow?: string;
  execution?: string;
  by?: string;
  at?: string;
  causation?: string;
  review: { status: 'accepted' | 'rejected'; by?: string; comment: string; at?: string; flow?: string; execution?: string; reviewId: string };
}

/** The verdicts a review submitted by an item decides, one per decided entry. */
export function reviewVerdicts(r: FoldedReview, it: ChangeItem, changeId?: string, seq?: number): DerivedVerdict[] {
  if (r.status !== 'submitted') return [];
  const out: DerivedVerdict[] = [];
  for (const e of r.entries ?? []) {
    if (e.outcome !== 'accept' && e.outcome !== 'reject') continue;
    const by = r.decidedBy[e.changeImpactId] || r.submittedBy || r.by || '';
    const at = r.decidedAt[e.changeImpactId] || it.createdAt || '';
    const status = e.outcome === 'accept' ? 'accepted' : 'rejected';
    out.push({
      id: `${it.id}/${e.changeImpactId}`,
      changeId,
      seq,
      impactId: e.changeImpactId,
      op: 'reviewed',
      flow: r.flow,
      execution: r.execution,
      by,
      at,
      causation: it.id,
      review: { status, by, comment: effectiveComment(r.comment ?? '', e.comment ?? ''), at, flow: r.flow, execution: r.execution, reviewId: r.key },
    });
  }
  return out;
}

/** The answer of a write put over what the change shows, until the stream brings the new version: a version newer than
 * the folded one wins. */
export function overlay(folded: ReviewRecord[], local: Record<string, ReviewRecord>): ReviewRecord[] {
  const seen = new Set<string>();
  const out = folded.map((r) => {
    seen.add(r.key);
    const l = local[r.key];
    return l && (l.versions ?? 0) > (r.versions ?? 0) ? l : r;
  });
  for (const l of Object.values(local)) if (!seen.has(l.key)) out.push(l);
  return out;
}

/** The reviews to list: open ones first, then submitted ones, in the order of opening within each. A discarded review
 * is gone from the list: it changed no impact, and only the audit trail (the change log) keeps it. */
export function reviewOrder(reviews: ReviewRecord[]): ReviewRecord[] {
  const rank = (r: ReviewRecord) => (r.status === 'open' ? 0 : 1);
  return reviews.filter((r) => r.status !== 'discarded').map((r, i) => ({ r, i })).sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i).map((x) => x.r);
}

const flowOf = (flow: string | undefined) => (!flow || flow === 'main' ? '' : flow);

/** The impacts a review of a flow may take: those the flow sees (pass the nodes of the scope), not replaced, whose
 * review is proposed and that no other open review of the same flow holds. except: the review being edited. */
export function awaitingImpacts(nodes: ChangeImpact[], reviews: ReviewRecord[], flow: string, except = ''): ChangeImpact[] {
  const f = flowOf(flow);
  const held = new Set<string>();
  for (const r of reviews) {
    if (r.status !== 'open' || flowOf(r.flow) !== f || r.key === except) continue;
    for (const e of r.entries ?? []) held.add(e.changeImpactId);
  }
  return nodes.filter((n) => !!n.id && !n.superseded && (n.review === 'proposed' || !n.review) && !held.has(n.id!));
}

/** Why the impact cannot be accepted yet: the validation rule its draft fails (the graph checks every draft as it stands,
 * the same gate as the accept), or that it modifies nothing (a candidate, a draft left as it was checked out: ADR 0102);
 * empty when it may be. */
export const invalidReason = (n: ChangeImpact | undefined): string => {
  if (!n) return '';
  if (n.validation && !n.validation.valid) return n.validation.message || 'the draft fails a validation rule';
  return notAcceptableWhy(n) ?? '';
};

/** The impacts of a list that pass their validation (or are not checked): the ones a review may accept. */
export const validImpacts = (list: ChangeImpact[]): ChangeImpact[] => list.filter((n) => !invalidReason(n));

/** The comment the review of an impact keeps once submitted: the entry's own, then the global one (the server's
 * review.EffectiveComment). Empty only when both are. */
export function effectiveComment(global: string, entry: string): string {
  const g = global.trim();
  const e = entry.trim();
  if (!e) return g;
  if (!g) return e;
  return `${e}\n\nReview: ${g}`;
}

/** What stops an open review from being submitted, in words; empty when it may be. ignore: entries left out (the stale
 * ones, which the panel removes before it submits). */
export function submitProblems(r: ReviewRecord, ignore: Set<string> = new Set(), nodes: ChangeImpact[] = []): string[] {
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const entries = (r.entries ?? []).filter((e) => !ignore.has(e.changeImpactId));
  const out: string[] = [];
  if (r.status !== 'open') return ['the review is final'];
  if (!entries.length) out.push('add at least one impact');
  for (const e of entries) {
    if (e.outcome !== 'accept' && e.outcome !== 'reject') out.push(`${e.changeImpactId}: accept or reject it`);
    else if (e.outcome === 'accept' && invalidReason(byId.get(e.changeImpactId))) out.push(`${e.changeImpactId}: cannot be accepted, ${invalidReason(byId.get(e.changeImpactId))}`);
    else if (!effectiveComment(r.comment ?? '', e.comment ?? '')) out.push(`${e.changeImpactId}: a comment is needed, its own or the review's`);
  }
  return out;
}

/** Whether an open review may be submitted: an entry at least and an outcome on every one (a comment on each, its own
 * or the global one). */
export const canSubmit = (r: ReviewRecord, ignore?: Set<string>, nodes?: ChangeImpact[]): boolean => submitProblems(r, ignore, nodes).length === 0;

/** A line of the table of a review: a change impact, in the review (included) or only awaiting one. */
export interface ReviewRow {
  id: string;
  /** the impact as the scope sees it (absent for an entry of a final review whose impact is not in the scope) */
  node?: ChangeImpact;
  included: boolean;
  comment: string;
  outcome: string;
}

const proposed = (n: ChangeImpact | undefined): n is ChangeImpact => !!n && !!n.id && !n.superseded && (n.review === 'proposed' || !n.review);

/** The entries of an open review whose impact no longer awaits a review (accepted or rejected elsewhere, replaced): the
 * table hides them and Submit removes them first. */
export function staleEntries(nodes: ChangeImpact[], r: ReviewRecord): string[] {
  if (r.status !== 'open') return [];
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  return (r.entries ?? []).filter((e) => !proposed(byId.get(e.changeImpactId))).map((e) => e.changeImpactId);
}

/** The table of a review. Open: every proposed impact of the scope, the entries of the review first (included), then the
 * impacts that await a review and no other open review holds (not included: the box to tick); stale entries are left
 * out. Final: the entries as they were reviewed. reviews: all the reviews of the change (the ones of other flows are
 * ignored). */
export function reviewRows(nodes: ChangeImpact[], reviews: ReviewRecord[], r: ReviewRecord): ReviewRow[] {
  const byId = new Map(nodes.map((n) => [n.id ?? '', n]));
  const entryRow = (e: ReviewEntry): ReviewRow => ({ id: e.changeImpactId, node: byId.get(e.changeImpactId), included: true, comment: e.comment ?? '', outcome: e.outcome ?? '' });
  if (r.status !== 'open') return (r.entries ?? []).map(entryRow);
  const stale = new Set(staleEntries(nodes, r));
  const rows = (r.entries ?? []).filter((e) => !stale.has(e.changeImpactId)).map(entryRow);
  const have = new Set(rows.map((x) => x.id));
  for (const n of awaitingImpacts(nodes, reviews, r.flow ?? '', r.key)) {
    if (!have.has(n.id!)) rows.push({ id: n.id!, node: n, included: false, comment: '', outcome: '' });
  }
  return rows;
}

/** How many entries accept, reject or are still undecided. */
export function tally(r: ReviewRecord): { accept: number; reject: number; undecided: number } {
  const t = { accept: 0, reject: 0, undecided: 0 };
  for (const e of r.entries ?? []) {
    if (e.outcome === 'accept') t.accept++;
    else if (e.outcome === 'reject') t.reject++;
    else t.undecided++;
  }
  return t;
}

/** Whether the principal may change the review: its author, or an administrator. */
export const mayEdit = (r: ReviewRecord, me: string, admin: boolean): boolean => r.status === 'open' && (admin || r.by === me);

/** Where a review stands in its life (ADR 0152): to create (impacts await a review and no review is open), open (a
 * draft its author builds up), submitted (final, read-only). */
export type ReviewStage = 'create' | 'open' | 'submitted';
export const REVIEW_STAGES: { id: ReviewStage; label: string }[] = [
  { id: 'create', label: 'To create' },
  { id: 'open', label: 'Open · draft' },
  { id: 'submitted', label: 'Submitted' },
];

/** The stage of a review record. */
export const stageOf = (r: ReviewRecord): ReviewStage => (r.status === 'open' ? 'open' : 'submitted');

/** One decision on a change impact, as its history shows it (ADR 0152). */
export interface Decision {
  at: string;
  /** accepted | rejected | proposed (proposed: sent back, an edit after a verdict) */
  status: string;
  by: string;
  comment: string;
  /** the review the verdict was submitted in (ADR 0105) */
  reviewId: string;
  /** the flow it was made on, '' for the main flow */
  flow: string;
  superseded: boolean;
  /** an accepted verdict passed the validation rules on the draft as it stood then: the graph refuses an accept that
   * fails them, in the transaction of the verdict (ADR 0079, checkAccepted) */
  checked: boolean;
}

/** The history of the decisions on a change impact, the latest first: every verdict and every return to proposed, as
 * the graph recorded them (ChangeImpact.reviews). */
export function decisionHistory(n: ChangeImpact | undefined): Decision[] {
  return (n?.reviews ?? [])
    .map((r) => ({
      at: r.at ?? '',
      status: r.status ?? '',
      by: r.by ?? '',
      comment: r.comment ?? '',
      reviewId: r.reviewId ?? '',
      flow: flowOf(r.flow),
      superseded: !!r.superseded,
      checked: r.status === 'accepted',
    }))
    .reverse();
}

/** The version a committed impact was written as, else undefined. */
export const committedAs = (n: ChangeImpact | undefined): NodeRef | undefined => (n?.committed?.id ? n.committed : undefined);
