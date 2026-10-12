import { describe, expect, it } from 'vitest';
import { attachable, detachable, guardianChain, parentChoices } from './changeAttach';
import type { Change } from './api';

describe('attach a change (ADR 0106)', () => {
  it('shows the chain when outer guardians frame the change', () => {
    expect(guardianChain({ guardians: [{ methodology: 'release-based' }, { methodology: 'sdlc' }] })).toBe('release-based › sdlc');
    expect(guardianChain({ guardians: [{ methodology: 'sdlc' }] })).toBe('');
    expect(guardianChain({})).toBe('');
  });

  it('offers attach to an open root change and detach to an open sub-change', () => {
    expect(attachable({ status: 'active' })).toBe(true);
    expect(attachable({ status: 'active', parentId: 'R' })).toBe(false);
    expect(attachable({ status: 'applied' })).toBe(false);
    expect(detachable({ status: 'draft', parentId: 'R' })).toBe(true);
    expect(detachable({ status: 'draft' })).toBe(false);
  });

  it('lists the open changes of another methodology, never itself nor what is under it', () => {
    const me: Change = { id: 'C', methodology: 'sdlc', namespace: 'alm', status: 'active' };
    const all: Change[] = [
      me,
      { id: 'R2', title: 'R2', methodology: 'release-based', namespace: 'alm', status: 'active' },
      { id: 'R1', title: 'R1', methodology: 'release-based', namespace: 'alm', status: 'draft' },
      { id: 'OLD', title: 'old', methodology: 'release-based', namespace: 'alm', status: 'applied' },
      { id: 'X', title: 'x', methodology: 'release-based', namespace: 'other', status: 'active' },
      { id: 'S', title: 's', methodology: 'review', namespace: 'alm', status: 'active', parentId: 'C' },
      { id: 'P', title: 'p', methodology: 'sdlc', namespace: 'alm', status: 'active' },
    ];
    expect(parentChoices(me, all).map((c) => c.id)).toEqual(['R1', 'R2']);
  });
});
