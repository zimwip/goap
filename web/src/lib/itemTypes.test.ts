import { describe, expect, it } from 'vitest';
import { filterItemTypes, itemTypeShownIn, originLabel } from './itemTypes';

describe('item types', () => {
  it('tells where the change tab shows a kind', () => {
    expect(itemTypeShownIn('risk')).toBe('a pane of its own in the change tab');
    expect(itemTypeShownIn('assessment')).toBe('a bubble next to the subject it is about');
    expect(itemTypeShownIn('signal')).toBe('the raw list of the Artifacts pane');
  });

  it('reads the origin', () => {
    expect(originLabel({ type: 'artifact', origin: 'core' })).toBe('built-in');
    expect(originLabel({ type: 'risk', origin: 'risk' })).toBe('use case risk');
    expect(originLabel({ type: 'x' })).toBe('registered');
  });

  it('filters by kind, origin and description', () => {
    const all = [
      { type: 'artifact', origin: 'core', description: 'A product of an action' },
      { type: 'risk', origin: 'risk', description: 'A risk of the change' },
    ];
    expect(filterItemTypes(all, '').length).toBe(2);
    expect(filterItemTypes(all, 'product').map((k) => k.type)).toEqual(['artifact']);
    expect(filterItemTypes(all, 'RISK').map((k) => k.type)).toEqual(['risk']);
  });
});
