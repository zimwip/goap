import { describe, expect, it } from 'vitest';
import { MAIN_SCOPE } from './changeScope';
import {
  clip,
  countByCategory,
  defaultSides,
  describeChange,
  formatValue,
  groupImpacts,
  identicalLine,
  reconcileSides,
  sameScope,
  scopeChoices,
  scopeOf,
  swapSides,
  toggleCategory,
  type Category,
} from './flowDiff';
import type { Flow, ImpactDiff } from './api';

const opt = (id: string, name: string, status = 'open'): Flow => ({ id, status, optionStatus: status === 'open' ? 'exploring' : status, option: { name } }) as Flow;
const a = opt('fa', 'Stripe');
const b = opt('fb', 'Adyen');
const old = opt('fo', 'Old', 'discarded');

describe('scope selectors', () => {
  it('offers the main flow, then the open options, then the decided ones', () => {
    expect(scopeChoices([old, a, b]).map((c) => c.id)).toEqual([MAIN_SCOPE, 'fa', 'fb', 'fo']);
    expect(scopeChoices([a])[0].label).toBe('Main flow');
    expect(scopeChoices([old])[1].status).toBe('discarded');
  });
  it('starts with the main flow against the option the scope bar looks at', () => {
    expect(defaultSides([a, b], 'fb')).toEqual({ left: MAIN_SCOPE, right: 'fb' });
  });
  it('else against the first open option', () => {
    expect(defaultSides([old, a, b], MAIN_SCOPE)).toEqual({ left: MAIN_SCOPE, right: 'fa' });
    expect(defaultSides([old, a], 'unknown')).toEqual({ left: MAIN_SCOPE, right: 'fa' });
  });
  it('else the first option, even decided; the main flow when there is none', () => {
    expect(defaultSides([old], MAIN_SCOPE).right).toBe('fo');
    expect(defaultSides([], MAIN_SCOPE)).toEqual({ left: MAIN_SCOPE, right: MAIN_SCOPE });
  });
  it('swaps, and knows the same scope on both sides', () => {
    expect(swapSides({ left: MAIN_SCOPE, right: 'fa' })).toEqual({ left: 'fa', right: MAIN_SCOPE });
    expect(sameScope({ left: 'fa', right: 'fa' })).toBe(true);
    expect(sameScope({ left: MAIN_SCOPE, right: 'fa' })).toBe(false);
  });
  it('keeps the sides, unless an option is gone', () => {
    const s = { left: 'fa', right: 'fb' };
    expect(reconcileSides(s, [a, b], MAIN_SCOPE)).toBe(s);
    expect(reconcileSides(s, [b], MAIN_SCOPE)).toEqual({ left: MAIN_SCOPE, right: 'fb' });
  });
});

const impacts: ImpactDiff[] = [
  { key: 'A', category: 'added' },
  { key: 'B', category: 'modified' },
  { key: 'C', category: 'modified' },
  { key: 'D', category: 'removed' },
];

describe('groups and filters', () => {
  it('counts each category', () => {
    expect(countByCategory(impacts)).toEqual({ added: 1, removed: 1, modified: 2 });
    expect(countByCategory([])).toEqual({ added: 0, removed: 0, modified: 0 });
  });
  it('groups in the order added, removed, modified', () => {
    expect(groupImpacts(impacts, new Set()).map((g) => [g.category, g.total, g.items.length])).toEqual([
      ['added', 1, 1],
      ['removed', 1, 1],
      ['modified', 2, 2],
    ]);
  });
  it('leaves empty categories out and hidden ones without items but with their count', () => {
    const g = groupImpacts(impacts.slice(0, 1), new Set());
    expect(g.map((x) => x.category)).toEqual(['added']);
    const h = groupImpacts(impacts, new Set<Category>(['modified']));
    expect(h.find((x) => x.category === 'modified')).toMatchObject({ total: 2, items: [] });
  });
  it('toggles a category without touching the filter it was given', () => {
    const none = new Set<Category>();
    const one = toggleCategory(none, 'added');
    expect([...one]).toEqual(['added']);
    expect(none.size).toBe(0);
    expect(toggleCategory(one, 'added').size).toBe(0);
  });
  it('words the hidden identical impacts', () => {
    expect(identicalLine(1)).toBe('1 identical impact hidden');
    expect(identicalLine(0)).toBe('0 identical impacts hidden');
    expect(identicalLine(12)).toBe('12 identical impacts hidden');
  });
  it('opens an impact in the scope that holds it', () => {
    const sides = { left: MAIN_SCOPE, right: 'fa' };
    expect(scopeOf({ category: 'added' }, sides)).toBe('fa');
    expect(scopeOf({ category: 'removed' }, sides)).toBe(MAIN_SCOPE);
    expect(scopeOf({ category: 'modified' }, sides)).toBe('fa');
  });
});

describe('values and changes', () => {
  it('formats values', () => {
    expect(formatValue(undefined)).toBe('—');
    expect(formatValue(null)).toBe('—');
    expect(formatValue('x')).toBe('x');
    expect(formatValue('')).toBe('""');
    expect(formatValue(3)).toBe('3');
    expect(formatValue({ a: [1] })).toBe('{"a":[1]}');
  });
  it('clips long values', () => {
    expect(clip('short', 10)).toEqual({ text: 'short', clipped: false });
    expect(clip('0123456789ab', 10)).toEqual({ text: '0123456789…', clipped: true });
  });
  it('words a property change as old to new', () => {
    expect(describeChange({ kind: 'property', name: 'title', op: 'changed', old: 'a', new: 'b' })).toEqual({ op: 'changed', label: 'title', old: 'a', new: 'b' });
    expect(describeChange({ kind: 'property', name: 'n', op: 'added', new: 3 })).toEqual({ op: 'added', label: 'n', old: undefined, new: '3' });
    expect(describeChange({ kind: 'property', name: 'n', op: 'removed', old: 'x' })).toEqual({ op: 'removed', label: 'n', old: 'x', new: undefined });
  });
  it('words the state, the owner and the links', () => {
    expect(describeChange({ kind: 'state', op: 'changed', old: 'draft', new: 'approved' }).label).toBe('state');
    expect(describeChange({ kind: 'owner', op: 'changed', old: 'A', new: 'B' }).label).toBe('owner');
    expect(describeChange({ kind: 'link', name: 'satisfies', op: 'added', target: 'NEED-1' })).toEqual({ op: 'added', label: 'link satisfies NEED-1' });
    expect(describeChange({ kind: 'link', name: 'satisfies', op: 'changed', target: 'NEED-1', old: { w: 1 }, new: { w: 2 } })).toMatchObject({ op: 'changed', old: '{"w":1}', new: '{"w":2}' });
  });
});
