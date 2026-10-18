import { describe, expect, it } from 'vitest';
import { stackLine, stackRows } from './changeStack';

describe('change-stack rows', () => {
  const stack = {
    changes: [
      { id: 'seed', title: 'Import' },
      { id: 'a', title: 'sub A', parent: 'p' },
      { id: 'b', title: 'sub B', parent: 'p' },
      { id: 'p', title: 'Parent' },
      { id: 'hidden00-0000' },
    ],
    versions: [
      { version: 1, branch: 'main', reason: 'create', change: 'seed' },
      { version: 2, branch: 'change-a', reason: 'derive', parents: [1], change: 'a', joined: [{ branch: 'change-p', change: 'a' }] },
      { version: 3, branch: 'change-b', reason: 'derive', parents: [1], change: 'b' },
      { version: 4, branch: 'change-b', reason: 'rebase', parents: [3, 2], change: 'b', joined: [{ branch: 'main', change: 'p' }] },
    ],
  };

  it('says what each change did, a sub-change under its parent', () => {
    const rows = stackRows(stack);
    expect(rows.map((r) => [r.id, r.depth, r.parent])).toEqual([
      ['seed', 0, undefined],
      ['a', 1, 'p'],
      ['b', 1, 'p'],
      ['p', 0, undefined],
      ['hidden00-0000', 0, undefined],
    ]);
    expect(rows[2].how).toEqual(['wrote v3 (derive) on change-b', 'rebased v4 (v3 + v2) on change-b']);
    expect(rows[3].how).toEqual(['made v4 join main']);
    expect(rows[4].label).toMatch(/^change /);
  });

  it('names the stack in one line', () => {
    expect(stackLine(stack)).toContain('sub B (sub-change of Parent) + Parent');
    expect(stackLine(undefined)).toBe('');
  });
});
