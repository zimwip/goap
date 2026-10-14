import { describe, expect, it } from 'vitest';
import { addressee, answeredInPlace, askKind, groupAsks, roleLabel, waitingFor } from './changeAsks';

describe('changeAsks', () => {
  it('splits the asks between the caller and the others, keeping their order', () => {
    const g = groupAsks([
      { id: 'a', mayAnswer: true },
      { id: 'b' },
      { id: 'c', mayAnswer: true },
    ]);
    expect(g.mine.map((a) => a.id)).toEqual(['a', 'c']);
    expect(g.others.map((a) => a.id)).toEqual(['b']);
    expect(groupAsks(undefined)).toEqual({ mine: [], others: [] });
  });

  it('says whom an ask is addressed to', () => {
    expect(addressee({ roles: ['release_manager', 'qa'] })).toBe('release manager or qa');
    expect(addressee({ owner: 'alice' }, 'alice')).toBe('you');
    expect(addressee({ owner: 'alice' }, 'bob')).toBe('alice');
    expect(addressee({})).toBe('anyone working on the change');
    expect(roleLabel('change-owner')).toBe('change owner');
  });

  it('labels the kinds and knows which are answered in place', () => {
    expect(askKind({ kind: 'approval' })).toBe('Approval');
    expect(askKind({ kind: 'custom' })).toBe('custom');
    expect(answeredInPlace({ kind: 'input', processId: 'p' })).toBe(true);
    expect(answeredInPlace({ kind: 'question', point: 'd' })).toBe(true);
    expect(answeredInPlace({ kind: 'decision', point: 'd' })).toBe(false);
  });

  it('tells how long the change has been waiting', () => {
    const now = Date.parse('2026-10-14T12:00:00Z');
    expect(waitingFor('2026-10-14T11:59:40Z', now)).toBe('just now');
    expect(waitingFor('2026-10-14T11:55:00Z', now)).toBe('5 min');
    expect(waitingFor('2026-10-14T09:00:00Z', now)).toBe('3 h');
    expect(waitingFor('2026-10-11T12:00:00Z', now)).toBe('3 d');
    expect(waitingFor(undefined, now)).toBe('');
  });
});
