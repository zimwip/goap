import { describe, expect, it } from 'vitest';
import { awaitingImpacts, canSubmit, effectiveComment, foldReviews, mayEdit, overlay, reviewOrder, submitProblems, tally } from './reviews';
import type { ChangeImpact, ChangeItem, ReviewRecord } from './api';

const item = (id: string, data: Record<string, unknown>, extra: Partial<ChangeItem> = {}): ChangeItem => ({ id, kind: 'review', status: 'proposed', producedBy: 'alice', data: data as ChangeItem['data'], ...extra });
const rec = (r: Partial<ReviewRecord>): ReviewRecord => ({ key: 'R', status: 'open', entries: [], ...r });

describe('foldReviews', () => {
  it('keeps the last version of each key, in the order the keys appeared', () => {
    const got = foldReviews([
      item('1', { key: 'R1', status: 'open', comment: 'first' }),
      item('2', { key: 'R2', status: 'open' }),
      item('3', { key: 'R1', status: 'open', comment: 'second', entries: [{ impact: 'a', outcome: 'accept', comment: 'c' }] }),
      { id: 'x', kind: 'artifact' },
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
  it('lists open reviews first', () => {
    const order = reviewOrder([rec({ key: 'a', status: 'submitted' }), rec({ key: 'b', status: 'discarded' }), rec({ key: 'c' }), rec({ key: 'd' })]);
    expect(order.map((r) => r.key)).toEqual(['c', 'd', 'a', 'b']);
  });
});
