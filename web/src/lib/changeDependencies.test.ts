import { describe, expect, it } from 'vitest';
import { dependenciesEditable, dependencyChoices, dependencyNote, withDependency, withoutDependency } from './changeDependencies';

describe('changeDependencies', () => {
  const all = [
    { id: 'R', title: 'Release', status: 'active' },
    { id: 'A', title: 'A', status: 'active', parentId: 'R' },
    { id: 'B', title: 'B', status: 'active', parentId: 'R', dependsOn: ['A'] },
    { id: 'S', title: 'Sub of B', status: 'draft', parentId: 'B' },
    { id: 'X', title: 'X', status: 'active' },
    { id: 'Z', title: 'Zed', status: 'abandoned' },
    { id: 'L', title: 'Committed', status: 'applied' },
  ];

  it('offers the open changes that are not itself, its ancestors, its sub-changes or already depended on', () => {
    expect(dependencyChoices(all[2], all).map((c) => c.id)).toEqual(['X']);
    expect(dependencyChoices(all[0], all).map((c) => c.id)).toEqual(['X']);
    expect(dependencyChoices(all[4], all).map((c) => c.id)).toEqual(['A', 'B', 'R', 'S']);
  });

  it('edits the list and says what a dependency waits for', () => {
    expect(withDependency(['A'], 'A')).toEqual(['A']);
    expect(withDependency(undefined, 'A')).toEqual(['A']);
    expect(withoutDependency(['A', 'B'], 'A')).toEqual(['B']);
    expect(dependencyNote({ satisfied: true })).toBe('committed where this change merges');
    expect(dependencyNote({ reason: 'not merged yet' })).toBe('not merged yet');
    expect(dependenciesEditable({ status: 'active' })).toBe(true);
    expect(dependenciesEditable({ status: 'committed' })).toBe(false);
  });
});
