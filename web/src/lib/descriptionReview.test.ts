import { describe, expect, it } from 'vitest';
import { accepted, assessmentOf, bubbleKey, commentItem, fingerprint, pending, raterRun, reviewState as state, subjectText, threadOf } from './descriptionReview';
import type { ChangeItem, Process } from './api';

const change = { id: 'C1', methodology: 'sdlc', title: 'Pay', intent: 'Pay in 3 installments' };
const rating = (id: string, score: number, rated: string, reply = ''): ChangeItem => ({
  id,
  kind: 'assessment',
  producedBy: 'description_review/rate',
  data: { key: 'description', score, verdict: 'unclear', rated, reply, findings: ['no scope', { text: 'why?', severity: 'major' }] },
});
const comment = (id: string, text: string): ChangeItem => ({ id, kind: 'comment', data: { about: 'description', text } });

describe('descriptionReview', () => {
  it('rates the current text and follows the thread', () => {
    const items = [rating('A1', 2, subjectText(change), 'say why'), comment('K1', 'it is for the shop')];
    let a = assessmentOf({ ...change, items });
    expect(a?.score).toBe(2);
    expect(a?.findings).toEqual([{ text: 'no scope', severity: '' }, { text: 'why?', severity: 'major' }]);
    expect(a?.current).toBe(true);
    expect(a?.answered).toBe(false);
    expect(state(a)).toBe('awaiting');
    items.push(rating('A2', 4, subjectText(change), 'clear now'));
    a = assessmentOf({ ...change, items });
    expect([a?.versions, a?.answered, accepted(a), state(a)]).toEqual([2, true, true, 'accepted']);
    expect(threadOf(items).map((m) => [m.kind, m.text])).toEqual([
      ['assessment', 'say why'],
      ['comment', 'it is for the shop'],
      ['assessment', 'clear now'],
    ]);
    // an edit of the description makes the rating stale
    expect(state(assessmentOf({ ...change, intent: 'other', items }))).toBe('stale');
    expect(state(assessmentOf({ ...change, items: [] }))).toBe('none');
  });

  it('accepts a rating by the fingerprint of the text, as the server does', () => {
    expect(fingerprint('abc')).toBe('1a47e90b');
    expect(fingerprint('é')).toBe(fingerprint('\u00e9'));
    const change = { title: 'T', intent: 'I' };
    const items: ChangeItem[] = [{ id: 'a', kind: 'assessment', data: { key: 'description', score: 4, rated: fingerprint(subjectText(change)) } } as ChangeItem];
    expect(assessmentOf({ ...change, items })?.current).toBe(true);
    expect(assessmentOf({ ...change, intent: 'J', items })?.current).toBe(false);
  });

  it('finds the run of the rater and writes a comment', () => {
    // the agent of the method wrote the rating, under the companion run of the reaction
    const runs = [
      { id: 'P1', changeId: 'C1', trigger: 'companion:sdlc/description_review/rate' },
      { id: 'P3', changeId: 'C1', parentId: 'P1', steps: [{ items: ['I1'] }] },
      { id: 'P2', changeId: 'C1', trigger: 'companion:risk/risk_watch' },
    ] as Process[];
    expect(raterRun(runs, change, 'I1')?.id).toBe('P1');
    expect(raterRun(runs, change, '')).toBeUndefined();
    expect(commentItem('  hello ')).toEqual({ kind: 'comment', type: 'comment', data: { about: 'description', text: 'hello' } });
  });

  it('keeps the bubble open until the rating is accepted', () => {
    expect(pending('none')).toBe(false);
    expect(pending('none', true)).toBe(true);
    expect(pending('refused')).toBe(true);
    expect(pending('awaiting')).toBe(true);
    expect(pending('stale')).toBe(true);
    expect(pending('accepted')).toBe(false);
  });

  it('shows a hidden bubble again once the rating or its thread moves', () => {
    const items = [rating('R1', 2, subjectText(change))];
    const a = assessmentOf({ ...change, items });
    const k = bubbleKey(a, threadOf(items));
    expect(bubbleKey(assessmentOf({ ...change, items }), threadOf(items))).toBe(k);
    const more = [...items, comment('C1', 'why?')];
    expect(bubbleKey(assessmentOf({ ...change, items: more }), threadOf(more))).not.toBe(k);
    expect(bubbleKey(assessmentOf({ ...change, intent: 'other', items }), threadOf(items))).not.toBe(k);
  });
});
