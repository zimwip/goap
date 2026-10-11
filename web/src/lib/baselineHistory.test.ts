import { describe, expect, it } from 'vitest';
import { badgesOf, historyEntries, kindOf } from './baselineHistory';

// base B on main; test1 and test2 both start from B, each commits on its own branch; test1 is applied first (a
// fast-forward onto main), then test2 (a merge of its branch into main)
const baselines = [
  { id: 'B', branch: 'main', createdAt: '2026-01-01T00:00:00Z' },
  { id: 'c1', branch: 'change-aaaa1111', parentId: 'B', changeId: 'aaaa1111-x', createdAt: '2026-01-02T00:00:00Z' },
  { id: 'c2', branch: 'change-bbbb2222', parentId: 'B', changeId: 'bbbb2222-x', createdAt: '2026-01-03T00:00:00Z' },
  { id: 'm1', branch: 'main', parentId: 'B', changeId: 'aaaa1111-x', createdAt: '2026-01-04T00:00:00Z' },
  { id: 'm2', branch: 'main', parentId: 'm1', mergedFrom: 'c2', changeId: 'bbbb2222-x', createdAt: '2026-01-05T00:00:00Z' },
];

describe('historyEntries', () => {
  const rows = historyEntries(baselines, 'main');
  const byId = new Map(rows.map((r) => [r.id, r]));

  it('lists the history newest first, the lanes of the changes included', () => {
    expect(rows.map((r) => r.id)).toEqual(['m2', 'm1', 'c2', 'c1', 'B']);
    expect(rows.map((r) => r.lane)).toEqual(['main', 'main', 'change-bbbb2222', 'change-aaaa1111', 'main']);
  });

  it('forks both changes from the same baseline', () => {
    expect(byId.get('c1')?.parents.map((p) => p.id)).toEqual(['B']);
    expect(byId.get('c2')?.parents.map((p) => p.id)).toEqual(['B']);
  });

  it('joins a change by a merge, and a fast-forward to the commit it took', () => {
    expect(byId.get('m2')?.parents.map((p) => p.id)).toEqual(['m1', 'c2']);
    expect(byId.get('m1')?.parents.map((p) => p.id)).toEqual(['B', 'c1']);
  });

  it('shows a change branch as its own commit, without lanes of other changes', () => {
    const own = historyEntries(baselines, 'change-bbbb2222');
    expect(own.map((r) => r.id)).toEqual(['c2', 'B']);
    expect(own.map((r) => r.lane)).toEqual(['change-bbbb2222', 'main']);
  });

  it('keeps a change that did not land out of the history of the branch', () => {
    expect(historyEntries(baselines.filter((b) => b.id !== 'm2'), 'main').map((r) => r.id)).not.toContain('c2');
  });
});

// ADR 0101: test2 is rebased onto T1 (it was forked from B), so its commit forks from T1 and remembers B; applying it
// is a fast-forward and main stays linear.
const rebased = [
  { id: 'B', branch: 'main', name: 'base', createdAt: '2026-01-01T00:00:00Z' },
  { id: 'c1', branch: 'change-aaaa1111', kind: 'commit', parentId: 'B', changeId: 'aaaa1111-x', createdAt: '2026-01-02T00:00:00Z' },
  { id: 'T1', branch: 'main', kind: 'fast-forward', parentId: 'B', changeId: 'aaaa1111-x', name: 'test1', createdAt: '2026-01-03T00:00:00Z' },
  { id: 'c2', branch: 'change-bbbb2222', kind: 'commit', parentId: 'T1', rebasedFrom: 'B', changeId: 'bbbb2222-x', createdAt: '2026-01-04T00:00:00Z' },
  { id: 'T2', branch: 'main', kind: 'fast-forward', parentId: 'T1', changeId: 'bbbb2222-x', createdAt: '2026-01-05T00:00:00Z' },
];

describe('a rebased change', () => {
  const label = (b: { changeId?: string }) => (b.changeId ? `change ${b.changeId.slice(0, 8)}` : undefined);
  const rows = historyEntries(rebased, 'main', '', label);
  const byId = new Map(rows.map((r) => [r.id, r]));

  it('forks from the head it was rebased onto and keeps a dashed edge to where it started', () => {
    const c2 = byId.get('c2');
    expect(c2?.parents.map((p) => p.id)).toEqual(['T1', 'B']);
    expect(c2?.parents.map((p) => p.label)).toEqual(['fork · change bbbb2222', 'rebased from · change bbbb2222']);
    expect(c2?.parents.map((p) => !!p.dashed)).toEqual([false, true]);
  });

  it('keeps main linear: each fast-forward follows the previous head and joins its commit', () => {
    expect(rows.map((r) => r.id)).toEqual(['T2', 'c2', 'T1', 'c1', 'B']);
    expect(byId.get('T2')?.parents.map((p) => p.id)).toEqual(['T1', 'c2']);
    expect(byId.get('T2')?.parents.map((p) => p.label)).toEqual(['fast-forward · change bbbb2222', 'fast-forward · change bbbb2222']);
  });

  it('shows the rebased-from baseline as a ghost row when no lane holds it', () => {
    const own = historyEntries(rebased, 'change-bbbb2222');
    expect(own.map((r) => r.id).sort()).toEqual(['B', 'c2', 'T1'].sort());
  });

  it('badges each row with its kind and the rebase', () => {
    expect(kindOf(rebased[1])).toBe('commit');
    expect(badgesOf(rebased[2], rebased).kind).toBe('fast-forward');
    expect(badgesOf(rebased[3], rebased)).toEqual({ kind: 'commit', rebased: 'rebased onto test1, was base' });
    expect(badgesOf(rebased[0], rebased).kind).toBe('snapshot');
  });

  it('names the change and branch a merge joined', () => {
    const b = badgesOf(baselines[4], baselines);
    expect(b.kind).toBe('merge');
    expect(b.merged).toBe('merged change bbbb2222 from change-bbbb2222');
  });
});
