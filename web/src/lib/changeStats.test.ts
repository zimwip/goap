import { describe, expect, it } from 'vitest';
import { counterGroups } from './changeStats';

describe('counterGroups', () => {
  it('keeps the counters that are not zero, grouped', () => {
    const g = counterGroups({ created: 2, deleted: 1, requalified: 1, linksAdded: 3, accepted: 1 });
    expect(g.nodes.map((c) => [c.key, c.value])).toEqual([['created', 2], ['deleted', 1], ['requalified', 1]]);
    expect(g.content.map((c) => c.key)).toEqual(['linksAdded']);
    expect(g.pending.map((c) => c.key)).toEqual(['accepted']);
    expect(g.empty).toBe(false);
  });
  it('is empty with no counter', () => {
    expect(counterGroups(undefined).empty).toBe(true);
    expect(counterGroups({}).empty).toBe(true);
  });
});
