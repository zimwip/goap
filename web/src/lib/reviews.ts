// The review object of a change (ADR 0080): a review built up while open (a global comment, one entry per change
// impact with its own comment and outcome), then submitted. It is an item of kind `review`, each version of the item
// the whole record of its data.key (the server's review.Reviews); this module folds the items and holds the pure rules
// the panel shows: which impacts await a review, when a review may be submitted, the comment each impact keeps.
import type { ChangeImpact, ChangeItem, ReviewEntry, ReviewRecord } from './api';

export const REVIEW_KIND = 'review';
export type Outcome = 'accept' | 'reject';
export const OUTCOMES: { id: Outcome; label: string }[] = [
  { id: 'accept', label: 'Accept' },
  { id: 'reject', label: 'Reject' },
];

const str = (v: unknown) => (typeof v === 'string' ? v.trim() : '');

/** The entries of the data of a review item. */
function entriesOf(data: Record<string, unknown> | undefined): ReviewEntry[] {
  const list = data?.entries;
  if (!Array.isArray(list)) return [];
  return list
    .filter((x): x is Record<string, unknown> => !!x && typeof x === 'object')
    .map((x) => ({ changeImpactId: str(x.impact), comment: str(x.comment), outcome: str(x.outcome) }));
}

/** The reviews of a change: the last version of each key, in the order the keys appeared. A submitted or discarded
 * review is final (a later version of its key is ignored); a rejected or superseded item is not a version. */
export function foldReviews(items: ChangeItem[]): ReviewRecord[] {
  const out: ReviewRecord[] = [];
  const at = new Map<string, number>();
  for (const it of items) {
    if (it.kind !== REVIEW_KIND || it.status === 'rejected' || it.status === 'superseded') continue;
    const data = it.data as Record<string, unknown> | undefined;
    const key = str(data?.key);
    const status = str(data?.status);
    if (!key || !['open', 'submitted', 'discarded'].includes(status)) continue;
    const r: ReviewRecord = {
      key,
      flow: it.flow ?? '',
      comment: str(data?.comment),
      status,
      entries: entriesOf(data),
      by: str(data?.by) || it.producedBy || '',
      submittedAt: str(data?.submittedAt),
      item: it.id,
      versions: 1,
    };
    const i = at.get(key);
    if (i === undefined) {
      at.set(key, out.length);
      out.push(r);
    } else if (out[i].status === 'open') {
      r.versions = (out[i].versions ?? 0) + 1;
      out[i] = r;
    }
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

/** Open reviews first, then submitted, then discarded; the order of opening within each. */
export function reviewOrder(reviews: ReviewRecord[]): ReviewRecord[] {
  const rank = (r: ReviewRecord) => (r.status === 'open' ? 0 : r.status === 'submitted' ? 1 : 2);
  return reviews.map((r, i) => ({ r, i })).sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i).map((x) => x.r);
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

/** The comment the review of an impact keeps once submitted: the entry's own, then the global one (the server's
 * review.EffectiveComment). Empty only when both are. */
export function effectiveComment(global: string, entry: string): string {
  const g = global.trim();
  const e = entry.trim();
  if (!e) return g;
  if (!g) return e;
  return `${e}\n\nReview: ${g}`;
}

/** What stops an open review from being submitted, in words; empty when it may be. */
export function submitProblems(r: ReviewRecord): string[] {
  const entries = r.entries ?? [];
  const out: string[] = [];
  if (r.status !== 'open') return ['the review is final'];
  if (!entries.length) out.push('add at least one impact');
  for (const e of entries) {
    if (e.outcome !== 'accept' && e.outcome !== 'reject') out.push(`${e.changeImpactId}: accept or reject it`);
    else if (!effectiveComment(r.comment ?? '', e.comment ?? '')) out.push(`${e.changeImpactId}: a comment is needed, its own or the review's`);
  }
  return out;
}

/** Whether an open review may be submitted: an entry at least and an outcome on every one (a comment on each, its own
 * or the global one). */
export const canSubmit = (r: ReviewRecord): boolean => submitProblems(r).length === 0;

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
