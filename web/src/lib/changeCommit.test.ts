import { describe, expect, it } from 'vitest';
import { acceptable, commitState, hasCommits, toCommit, uncommitted } from './changeCommit';
import type { Change, ChangeImpact } from './api';

const draft = { id: 'n1', version: 0 };
const modified: ChangeImpact = { id: 'i1', key: 'REQ-1', post: draft, review: 'accepted', validation: { valid: true, modified: true } };
const unchanged: ChangeImpact = { id: 'i2', key: 'REQ-2', post: { id: 'n2', version: 0 }, review: 'proposed', validation: { valid: true, modified: false } };
const candidate: ChangeImpact = { id: 'i3', key: 'REQ-3', pre: { id: 'n3', version: 1 }, review: 'proposed' };
const committed: ChangeImpact = { id: 'i4', key: 'REQ-4', post: { id: 'n4', version: 2 }, landed: { id: 'n4', version: 2 }, review: 'accepted' };

describe('changeCommit', () => {
  it('accepts only a modification, or the source of a merge', () => {
    expect([modified, unchanged, candidate, committed].map(acceptable)).toEqual([true, false, false, false]);
    expect(acceptable({ ...candidate, via: 'i9' })).toBe(true);
  });

  it('commits the accepted modifications', () => {
    expect(toCommit([modified, unchanged, candidate, committed]).map((c) => c.id)).toEqual(['i1']);
    expect(uncommitted([modified, unchanged, candidate, committed]).map((c) => c.id)).toEqual(['i1']);
  });

  it('enables Commit only with something to commit, Merge only with a commit and nothing pending', () => {
    const open: Change = { id: 'c', status: 'active' };
    expect(commitState(open, [unchanged, candidate], [])).toMatchObject({ canCommit: false, canMerge: false });
    expect(commitState(open, [modified], [])).toMatchObject({ canCommit: true, canMerge: false });
    const withCommit: Change = { ...open, resultBaselineId: 'b1' };
    expect(hasCommits(withCommit)).toBe(true);
    expect(commitState(withCommit, [committed, candidate], [])).toMatchObject({ canCommit: false, canMerge: true });
    expect(commitState(withCommit, [modified], [])).toMatchObject({ canCommit: true, canMerge: false });
    expect(commitState({ ...withCommit, status: 'applied' }, [], [])).toMatchObject({ canCommit: false, canMerge: false });
    expect(hasCommits({ ...withCommit, parentId: 'p' })).toBe(false);
  });
});
