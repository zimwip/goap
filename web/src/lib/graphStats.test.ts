import { describe, expect, it } from 'vitest';
import { byStream, fmt, insights, measure, num, overviewTotals, pct, ranked, shortType, total, versionHistogram } from './graphStats';
import type { GraphStats } from './api';

describe('graph statistics helpers', () => {
  it('reads the numbers of the wire', () => {
    expect(num('NaN')).toBe(0);
    expect(num(undefined)).toBe(0);
    expect(num(2.5)).toBe(2.5);
    expect(fmt(12345)).toBe('12,345');
    expect(fmt(1.2345, 2)).toBe('1.23');
    expect(pct(1, 4)).toBe('25.0%');
    expect(pct(1, 0)).toBe('–');
    expect(shortType('alm@Requirement')).toBe('Requirement');
    expect(shortType('Doc')).toBe('Doc');
  });

  it('ranks, groups and folds counts', () => {
    expect(ranked({ b: 2, a: 2, '': 5 }, 'edit')).toEqual([['edit', 5], ['a', 2], ['b', 2]]);
    expect(byStream({ 'fact.artifact': 2, 'fact.risk': 1, 'impact.created': 4 })).toEqual([['impact', 4], ['fact', 3]]);
    expect(versionHistogram({ '1': 10, '3': 2, '2': 4, '12': 1, '40': 1 })).toEqual([['1', 10], ['2', 4], ['3', 2], ['10+', 2]]);
    expect(total({ a: 1, b: 2 })).toBe(3);
    const s: GraphStats = { store: [{ name: 'tags', counts: { r: 2 } }] };
    expect(measure(s, 'tags')).toEqual({ r: 2 });
    expect(measure(s, 'joins')).toEqual({});
  });

  it('adds up the namespaces', () => {
    expect(overviewTotals([{ namespace: 'a', nodes: 2, versions: 3 }, { namespace: 'b', nodes: 1, changes: 4 }])).toEqual({
      nodes: 3, versions: 3, linkRows: 0, changes: 4, commits: 0, branches: 0, logEntries: 0,
    });
  });

  it('says what the shape of a state tells', () => {
    expect(insights(undefined)).toEqual([]);
    const lines = insights({
      state: {
        nodes: 4, links: 2, suspect: 1, isolated: 1, componentCount: 2, components: [3, 1],
        hubs: [{ key: 'REQ-1', type: 'alm@Requirement', out: 1, in: 1 }],
        types: [{ type: 'alm@Need', nodes: 2 }, { type: 'alm@Requirement', nodes: 1 }],
        versions: { mean: 1.25, max: 2 },
      },
    });
    expect(lines).toContain('The largest connected part holds 75.0% of the nodes, across 2 parts.');
    expect(lines).toContain('1 node has no link at all (25.0%).');
    expect(lines.some((l) => l.startsWith('1 link points to an older version'))).toBe(true);
    expect(lines).toContain('REQ-1 (Requirement) is the most connected node: 2 links.');
    expect(lines).toContain('Need is the most common type: 50.0% of the nodes.');
  });
});
