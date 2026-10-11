import { describe, expect, it } from 'vitest';
import { changeOfBranch, logicalTimes, shownBranches } from './branchFilter';

const b = (name: string, status: string, origin?: string, createdAt?: string) => ({ name, status, origin, createdAt });

describe('shownBranches', () => {
  const all = [
    b('change-ab12cd34', 'merged', 'change:ab12cd34-x', '2026-03-01T10:00:00Z'),
    b('main', 'open', undefined, '2026-01-01T10:00:00Z'),
    b('flow-1', 'open'),
    b('dev', 'abandoned', undefined, '2026-02-01T10:00:00Z'),
    b('feat', 'open', undefined, '2026-04-01T10:00:00Z'),
  ];

  it('lists open branches only by default, main first', () => {
    expect(shownBranches(all, false).map((x) => x.name)).toEqual(['main', 'feat']);
  });

  it('adds merged and abandoned ones, change branches included, flow branches never', () => {
    expect(shownBranches(all, true).map((x) => x.name)).toEqual(['main', 'feat', 'change-ab12cd34', 'dev']);
  });

  it('names the change that owns a branch', () => {
    expect(changeOfBranch(all[0])).toBe('ab12cd34-x');
    expect(changeOfBranch(all[1])).toBe('');
  });
});

describe('logical time', () => {
  // b1 <- b2 <- b3 on main; a branch forked at b1 was created last by the clock, one forked at b3 first
  const baselines = [
    { id: 'b1' },
    { id: 'b2', parentId: 'b1' },
    { id: 'b3', parentId: 'b2' },
    { id: 'c1', parentId: 'b1' },
    { id: 'b4', parentId: 'b3', mergedFrom: 'c1' },
  ];

  it('counts the longest history behind a baseline', () => {
    const t = logicalTimes(baselines);
    expect([...['b1', 'b2', 'b3', 'c1', 'b4']].map((id) => t.get(id))).toEqual([1, 2, 3, 2, 4]);
  });

  it('orders branches by where they forked in the history, not by their clock', () => {
    const branches = [
      { name: 'main', status: 'open' },
      { name: 'old-fork', status: 'open', forkBaseline: 'b1', createdAt: '2026-09-01T00:00:00Z' },
      { name: 'late-fork', status: 'open', forkBaseline: 'b3', createdAt: '2026-01-01T00:00:00Z' },
      { name: 'from-empty', status: 'open', createdAt: '2026-12-01T00:00:00Z' },
    ];
    expect(shownBranches(branches, false, baselines).map((x) => x.name)).toEqual(['main', 'late-fork', 'old-fork', 'from-empty']);
  });

  it('survives a cycle in corrupt data', () => {
    expect(logicalTimes([{ id: 'x', parentId: 'y' }, { id: 'y', parentId: 'x' }]).size).toBe(2);
  });
});
