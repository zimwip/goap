import { describe, expect, it } from 'vitest';
import { commitButtons, commitSelection, modifies, notAcceptableWhy, unmodified } from './changeCommit';
import type { ChangeImpact } from './api';

const draft = { id: 'n1', version: 0 };
const modified: ChangeImpact = { id: 'i1', key: 'REQ-1', post: draft, review: 'proposed', validation: { valid: true, modified: true } };
const unchanged: ChangeImpact = { id: 'i2', key: 'REQ-2', post: { id: 'n2', version: 0 }, review: 'proposed', validation: { valid: true, modified: false } };
const candidate: ChangeImpact = { id: 'i3', key: 'REQ-3', pre: { id: 'n3', version: 1 }, review: 'proposed' };

describe('changeCommit', () => {
  it('accepts only a modification', () => {
    expect([modified, unchanged, candidate].map(modifies)).toEqual([true, false, false]);
    expect(notAcceptableWhy(modified)).toBeUndefined();
    expect(notAcceptableWhy(unchanged)).toMatch(/checked out from/);
    expect(notAcceptableWhy(candidate)).toMatch(/candidate/);
  });

  it('words the state the server gives', () => {
    expect(commitButtons({ canCommit: true, toCommit: ['REQ-1'], reverts: 1, unsettled: ['REQ-1 is not committed'] }, false)).toEqual({
      canCommit: true,
      commitWhat: 'REQ-1, 1 withdrawn node(s) reverted',
      canMerge: false,
      mergeWhy: 'Settle first: REQ-1 is not committed',
    });
    expect(commitButtons({ commits: true, canMerge: true }, false)).toMatchObject({ canCommit: false, canMerge: true });
    expect(commitButtons({}, false).mergeWhy).toMatch(/Nothing committed/);
    expect(commitButtons({ canCommit: true, canMerge: true }, true)).toMatchObject({ canCommit: false, canMerge: false });
  });
  it('reads an unmodified draft as the server sends it (false is omitted from the JSON)', () => {
    const wire: ChangeImpact = { id: 'i3', key: 'LLA-1', post: { id: 'n3', version: 0 }, review: 'proposed', validation: { valid: true } };
    expect(unmodified(wire)).toBe(true);
    expect(modifies(wire)).toBe(false);
    expect(unmodified({ id: 'i4', post: { id: 'n4', version: 0 } })).toBe(false);
  });
});

describe('commitSelection', () => {
  const candidates = [
    { nodeId: 'a', key: 'A', kind: 'modified' },
    { nodeId: 'b', key: 'B', kind: 'created' },
  ];
  it('names no node when every candidate is chosen', () => {
    expect(commitSelection(candidates, new Set(['b', 'a']))).toEqual({ nodes: [] });
  });
  it('names the nodes chosen, in the order of the candidates', () => {
    expect(commitSelection(candidates, new Set(['b']))).toEqual({ nodes: ['b'] });
  });
  it('is nothing when none is chosen', () => {
    expect(commitSelection(candidates, new Set())).toBeUndefined();
  });
});
