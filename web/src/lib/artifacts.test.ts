import { describe, expect, it } from 'vitest';
import { artifactsOf, decisionsByItem, rawItems } from './artifacts';
import type { ChangeItem } from './api';

const items: ChangeItem[] = [
  { id: 'a1', kind: 'artifact' },
  { id: 'a2', kind: 'artifact' },
  { id: 'd1', kind: 'decision', createdAt: '2026-01-01T00:00:00Z', decision: { item: 'a1', accept: false, comment: 'no' } },
  { id: 'd2', kind: 'decision', createdAt: '2026-01-02T00:00:00Z', decision: { item: 'a1', accept: true, comment: 'yes' } },
  { id: 'd3', kind: 'decision', decision: { item: 'x9', accept: true } },
  { id: 'r', kind: 'review' },
  { id: 'm', kind: 'merge' },
  { id: 's', kind: 'signal' },
];

describe('artifacts', () => {
  it('keeps the artifacts only', () => expect(artifactsOf(items).map((i) => i.id)).toEqual(['a1', 'a2']));
  it('groups decisions per item, latest first', () => {
    const m = decisionsByItem(items);
    expect(m.get('a1')?.map((d) => d.id)).toEqual(['d2', 'd1']);
    expect(m.get('a2')).toBeUndefined();
    expect(m.get('x9')?.length).toBe(1);
  });
  it('lists the raw kinds without a pane', () => expect(rawItems(items).map((i) => i.id)).toEqual(['m', 's']));
});
