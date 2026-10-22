import { describe, expect, it } from 'vitest';
import { cellNodes, cellOf, matchesFilter, queryHood, querySummary, valueText } from './graphQuery';
import { hoodIndex } from './graphIndex';
import type { QueryResult } from './api';

const res: QueryResult = {
  columns: ['n', 'r', 'p', 'k'],
  rows: [[{ $node: 'n1' }, { $link: 'l1' }, { $path: { nodes: ['n1', 'n2'], links: ['l1'] } }, 'REQ-1']],
  nodes: [
    { id: 'n1', key: 'REQ-1', type: 'alm@Requirement', props: { title: 'Pay online' } },
    { id: 'n2', key: 'NEED-1', type: 'alm@Need' },
  ],
  links: [{ id: 'l1', type: 'alm@satisfies', from: { id: 'n1', version: 1 }, to: { id: 'n2', version: 1 } }],
  reads: 3,
};

describe('graph query cells', () => {
  it('reads nodes, links, paths and values', () => {
    const [n, r, p, k] = res.rows![0].map((v) => cellOf(v, res));
    expect(n).toMatchObject({ kind: 'node', id: 'n1', node: { key: 'REQ-1' } });
    expect(r).toMatchObject({ kind: 'link', id: 'l1' });
    expect(p).toEqual({ kind: 'path', nodes: ['n1', 'n2'], links: ['l1'] });
    expect(k).toEqual({ kind: 'value', value: 'REQ-1' });
    expect(cellNodes(r)).toEqual(['n1', 'n2']);
    expect(cellNodes(p)).toEqual(['n1', 'n2']);
    expect(cellOf({ $node: 'x', other: 1 })).toMatchObject({ kind: 'value' });
  });

  it('writes values as text', () => {
    expect(valueText(res.rows![0][0], res)).toBe('Pay online');
    expect(valueText(res.rows![0][1], res)).toBe('alm@satisfies');
    expect(valueText(null)).toBe('null');
    expect(valueText([1, 'a', { $node: 'n2' }], res)).toBe('[1, a, NEED-1]');
    expect(valueText({ a: 1 })).toBe('{"a":1}');
  });

  it('lays the answer into the explorer, its nodes not expanded', () => {
    const index = hoodIndex('', [queryHood(res)]);
    expect(index.list.map((n) => n.id).sort()).toEqual(['n1', 'n2']);
    expect(index.out.get('n1')?.length).toBe(1);
    expect(index.loaded.size).toBe(0);
    expect(querySummary({ ...res, truncated: true })).toBe('1 row · 2 nodes · 1 links · 3 reads · stopped by a cap');
  });

  it('filters the nodes of a Graph pane', () => {
    expect(matchesFilter(res.nodes![0], 'pay')).toBe(true);
    expect(matchesFilter(res.nodes![1], 'need')).toBe(true);
    expect(matchesFilter(res.nodes![1], 'pay')).toBe(false);
  });
});
