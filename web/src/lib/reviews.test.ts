import { describe, expect, it } from 'vitest';
import { awaitingImpacts, canSubmit, decisionHistory, invalidReason, stageOf, validImpacts, reviewRows, staleEntries, effectiveComment, foldReviews, mayEdit, overlay, reviewOrder, submitProblems, tally } from './reviews';
import type { ChangeImpact, ChangeItem, ReviewRecord } from './api';

const item = (id: string, data: Record<string, unknown>, extra: Partial<ChangeItem> = {}): ChangeItem => ({ id, type: 'review', status: 'proposed', producedBy: 'alice', data: data as ChangeItem['data'], ...extra });
const rec = (r: Partial<ReviewRecord>): ReviewRecord => ({ key: 'R', status: 'open', entries: [], ...r });

describe('foldReviews', () => {
  it('keeps the last version of each key, in the order the keys appeared', () => {
    const got = foldReviews([
      item('1', { key: 'R1', status: 'open', comment: 'first' }),
      item('2', { key: 'R2', status: 'open' }),
      item('3', { key: 'R1', status: 'open', comment: 'second', entries: [{ impact: 'a', outcome: 'accept', comment: 'c' }] }),
      { id: 'x', type: 'artifact' },
    ]);
    expect(got.map((r) => r.key)).toEqual(['R1', 'R2']);
    expect(got[0]).toMatchObject({ comment: 'second', versions: 2, item: '3', by: 'alice', entries: [{ changeImpactId: 'a', outcome: 'accept', comment: 'c' }] });
  });

  it('treats a submitted or discarded review as final', () => {
    const got = foldReviews([
      item('1', { key: 'R1', status: 'open' }),
      item('2', { key: 'R1', status: 'submitted', submittedAt: '2026-01-01T00:00:00Z', entries: [{ impact: 'a', outcome: 'reject' }] }),
      item('3', { key: 'R1', status: 'open', comment: 'sneaky' }),
      item('4', { key: 'R2', status: 'discarded' }),
      item('5', { key: 'R3', status: 'open' }, { status: 'rejected' }),
      item('6', { key: '', status: 'open' }),
      item('7', { key: 'R4', status: 'weird' }),
    ]);
    expect(got.map((r) => [r.key, r.status, r.comment])).toEqual([
      ['R1', 'submitted', ''],
      ['R2', 'discarded', ''],
    ]);
  });
});

describe('awaitingImpacts', () => {
  const nodes: ChangeImpact[] = [
    { id: 'a', review: 'proposed' },
    { id: 'b', review: 'accepted' },
    { id: 'c', review: 'proposed' },
    { id: 'd', review: 'proposed', superseded: true },
    { id: 'e' },
  ];
  it('lists the impacts awaiting review that no other open review holds', () => {
    const reviews = [rec({ key: 'R1', entries: [{ changeImpactId: 'c' }] }), rec({ key: 'R2', status: 'submitted', entries: [{ changeImpactId: 'a' }] })];
    expect(awaitingImpacts(nodes, reviews, 'main').map((n) => n.id)).toEqual(['a', 'e']);
    expect(awaitingImpacts(nodes, reviews, '', 'R1').map((n) => n.id)).toEqual(['a', 'c', 'e']);
  });
  it('holds an impact per flow', () => {
    const reviews = [rec({ key: 'R1', flow: 'f1', entries: [{ changeImpactId: 'a' }] })];
    expect(awaitingImpacts(nodes, reviews, 'main').map((n) => n.id)).toContain('a');
    expect(awaitingImpacts(nodes, reviews, 'f1').map((n) => n.id)).not.toContain('a');
  });
});

describe('submitting', () => {
  it('needs an entry, an outcome on each and a comment somewhere', () => {
    expect(canSubmit(rec({}))).toBe(false);
    expect(submitProblems(rec({}))).toEqual(['add at least one impact']);
    const undecided = rec({ comment: 'g', entries: [{ changeImpactId: 'a', outcome: 'accept' }, { changeImpactId: 'b' }] });
    expect(canSubmit(undecided)).toBe(false);
    expect(submitProblems(undecided)).toEqual(['b: accept or reject it']);
    expect(canSubmit(rec({ comment: 'g', entries: [{ changeImpactId: 'a', outcome: 'accept' }, { changeImpactId: 'b', outcome: 'reject' }] }))).toBe(true);
    // no comment at all: the review of the impact would have none
    expect(canSubmit(rec({ entries: [{ changeImpactId: 'a', outcome: 'accept' }] }))).toBe(false);
    expect(canSubmit(rec({ entries: [{ changeImpactId: 'a', outcome: 'accept', comment: 'mine' }] }))).toBe(true);
    expect(canSubmit(rec({ status: 'submitted', comment: 'g', entries: [{ changeImpactId: 'a', outcome: 'accept' }] }))).toBe(false);
  });

  it('keeps both comments', () => {
    expect(effectiveComment('', '')).toBe('');
    expect(effectiveComment(' g ', '')).toBe('g');
    expect(effectiveComment('', ' e ')).toBe('e');
    expect(effectiveComment('g', 'e')).toBe('e\n\nReview: g');
  });

  it('tells the outcomes', () => {
    expect(tally(rec({ entries: [{ changeImpactId: 'a', outcome: 'accept' }, { changeImpactId: 'b', outcome: 'reject' }, { changeImpactId: 'c' }] }))).toEqual({ accept: 1, reject: 1, undecided: 1 });
  });
});

describe('who may edit', () => {
  it('is the author or an administrator, while open', () => {
    expect(mayEdit(rec({ by: 'alice' }), 'alice', false)).toBe(true);
    expect(mayEdit(rec({ by: 'alice' }), 'bob', false)).toBe(false);
    expect(mayEdit(rec({ by: 'alice' }), 'bob', true)).toBe(true);
    expect(mayEdit(rec({ by: 'alice', status: 'submitted' }), 'alice', true)).toBe(false);
  });
});

describe('overlay and order', () => {
  it('prefers a newer version of the answer of a write', () => {
    const folded = [rec({ key: 'R1', versions: 2, comment: 'old' })];
    expect(overlay(folded, { R1: rec({ key: 'R1', versions: 3, comment: 'new' }) })[0].comment).toBe('new');
    expect(overlay(folded, { R1: rec({ key: 'R1', versions: 2, comment: 'same' }) })[0].comment).toBe('old');
    expect(overlay(folded, { R9: rec({ key: 'R9', versions: 1 }) }).map((r) => r.key)).toEqual(['R1', 'R9']);
  });
  it('lists open reviews first and leaves a discarded one out', () => {
    const order = reviewOrder([rec({ key: 'a', status: 'submitted' }), rec({ key: 'b', status: 'discarded' }), rec({ key: 'c' }), rec({ key: 'd' })]);
    expect(order.map((r) => r.key)).toEqual(['c', 'd', 'a']);
  });
});

describe('the table of a review', () => {
  const nodes: ChangeImpact[] = [
    { id: 'a', key: 'A', review: 'proposed' },
    { id: 'b', key: 'B', review: 'accepted' },
    { id: 'c', key: 'C', review: 'proposed' },
    { id: 'd', key: 'D', review: 'proposed' },
  ];
  const open = rec({ key: 'R1', entries: [{ changeImpactId: 'a', comment: 'x', outcome: 'accept' }, { changeImpactId: 'b', outcome: 'reject' }] });
  const other = rec({ key: 'R2', entries: [{ changeImpactId: 'd' }] });

  it('shows the entries still proposed, then the proposed impacts out of the review', () => {
    const rows = reviewRows(nodes, [open, other], open);
    expect(rows.map((r) => [r.id, r.included, r.comment, r.outcome])).toEqual([
      ['a', true, 'x', 'accept'],
      ['c', false, '', ''],
    ]);
  });
  it('finds the stale entries', () => {
    expect(staleEntries(nodes, open)).toEqual(['b']);
    expect(staleEntries(nodes, rec({ status: 'submitted', entries: [{ changeImpactId: 'b' }] }))).toEqual([]);
  });
  it('ignores the stale entries when it counts what stops a submit', () => {
    expect(canSubmit(open)).toBe(false);
    expect(canSubmit(open, new Set(['b']))).toBe(true);
    const bad = rec({ comment: 'g', entries: [{ changeImpactId: 'a', outcome: 'accept' }, { changeImpactId: 'b' }] });
    expect(canSubmit(bad)).toBe(false);
    expect(canSubmit(bad, new Set(['b']))).toBe(true);
    expect(canSubmit(rec({ comment: 'g', entries: [{ changeImpactId: 'b', outcome: 'accept' }] }), new Set(['b']))).toBe(false);
  });
  it('shows a final review as it was reviewed', () => {
    const done = rec({ status: 'submitted', entries: [{ changeImpactId: 'b', outcome: 'reject', comment: 'no' }] });
    expect(reviewRows(nodes, [done], done).map((r) => [r.id, r.included, r.outcome])).toEqual([['b', true, 'reject']]);
  });
});

describe('validation of an impact', () => {
  const draft = { id: 'n', version: 0 };
  const ok: ChangeImpact = { id: 'a', review: 'proposed', post: draft, validation: { valid: true, modified: true } };
  const bad: ChangeImpact = { id: 'b', review: 'proposed', post: draft, validation: { valid: false, message: 'alias reasoning needs a target (provider/model)' } };
  const unchecked: ChangeImpact = { id: 'c', review: 'proposed', post: draft };

  it('says why an impact cannot be accepted, and nothing for a valid or unchecked one', () => {
    expect(invalidReason(bad)).toBe('alias reasoning needs a target (provider/model)');
    expect(invalidReason(ok)).toBe('');
    expect(invalidReason(unchecked)).toBe('');
    expect(invalidReason({ id: 'd', validation: { valid: false } })).not.toBe('');
    // ADR 0102: a candidate or a draft that modifies nothing is not accepted
    expect(invalidReason({ id: 'e', review: 'proposed', pre: { id: 'n', version: 1 } })).toMatch(/modifies nothing/);
    expect(invalidReason({ ...ok, validation: { valid: true, modified: false } })).toMatch(/modifies nothing/);
    // ADR 0152: a committed impact is not a candidate (its post is the version its draft was written as)
    expect(invalidReason({ id: 'f', review: 'accepted', pre: { id: 'n', version: 1 }, post: { id: 'n', version: 2 }, committed: { id: 'n', version: 2 } })).toBe('');
    expect(invalidReason({ id: 'g', review: 'accepted', post: { id: 'n', version: 1 } })).toBe('');
  });

  it('keeps the impacts that pass', () => {
    expect(validImpacts([ok, bad, unchecked]).map((n) => n.id)).toEqual(['a', 'c']);
  });

  it('refuses to submit an entry that accepts an invalid impact', () => {
    const r = rec({ comment: 'ok', entries: [{ changeImpactId: 'b', outcome: 'accept' }] });
    expect(canSubmit(r, undefined, [ok, bad])).toBe(false);
    expect(submitProblems(r, new Set(), [ok, bad])[0]).toContain('alias reasoning needs a target');
    expect(canSubmit(rec({ comment: 'ok', entries: [{ changeImpactId: 'b', outcome: 'reject' }] }), undefined, [ok, bad])).toBe(true);
    expect(canSubmit(rec({ comment: 'ok', entries: [{ changeImpactId: 'a', outcome: 'accept' }] }), undefined, [ok, bad])).toBe(true);
  });
});

describe('decision history', () => {
  it('lists the decisions the latest first, an accepted verdict having passed the rules then', () => {
    const n: ChangeImpact = {
      id: 'a',
      reviews: [
        { status: 'rejected', by: 'bob', comment: 'missing link', at: '2026-10-21T07:30:00Z', reviewId: 'REV-1' },
        { status: 'proposed', by: 'alice', comment: 'edited again', at: '2026-10-21T07:50:00Z' },
        { status: 'accepted', by: 'carol', comment: 'ok', at: '2026-10-21T08:11:00Z', reviewId: 'REV-2', flow: 'main' },
      ],
    };
    const h = decisionHistory(n);
    expect(h.map((d) => d.status)).toEqual(['accepted', 'proposed', 'rejected']);
    expect(h[0]).toMatchObject({ by: 'carol', reviewId: 'REV-2', flow: '', checked: true });
    expect(h.slice(1).every((d) => !d.checked)).toBe(true);
    expect(decisionHistory(undefined)).toEqual([]);
  });

  it('tells the stage of a review', () => {
    expect(stageOf(rec({ status: 'open' }))).toBe('open');
    expect(stageOf(rec({ status: 'submitted' }))).toBe('submitted');
  });
});
