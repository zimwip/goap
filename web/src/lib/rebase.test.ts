import { describe, expect, it } from 'vitest';
import { behindLine, conflictLabel, conflictLines, rebasedOnto, syncVisible } from './rebase';

describe('conflictLabel', () => {
  it('reads the conflict names of a rebase', () => {
    expect(conflictLabel('props.title')).toBe('property “title”');
    expect(conflictLabel('state')).toBe('lifecycle state');
    expect(conflictLabel('owner')).toBe('owner');
    expect(conflictLabel('node')).toBe('the parent rejected or withdrew this node');
    expect(conflictLabel('alm@verifies:0123456789ab')).toBe('alm@verifies:0123456789ab');
    expect(conflictLabel('link:alm@verifies:0123456789ab')).toBe('link alm@verifies → 01234567');
  });
});

describe('root change behind its target (ADR 0101)', () => {
  it('says where the branch stands', () => {
    expect(behindLine('main', 'T1T1T1T1-22', [])).toBe('Behind main (now at T1T1T1T1).');
    expect(behindLine('main', 'T1', ['REQ-A', 'REQ-B'])).toBe('Behind main (now at T1): REQ-A, REQ-B changed there since this change took them.');
  });

  it('shows the pane only when there is something to say', () => {
    const none = { isSub: false, behind: 0, onto: '', conflicts: 0, error: '' };
    expect(syncVisible(none)).toBe(false);
    expect(syncVisible({ ...none, onto: 'T1' })).toBe(true);
    expect(syncVisible({ ...none, conflicts: 1 })).toBe(true);
    expect(syncVisible({ ...none, isSub: true, onto: 'T1' })).toBe(false);
    expect(syncVisible({ ...none, isSub: true, behind: 1 })).toBe(true);
  });
});

describe('rebase task of a run', () => {
  it('lists the conflicts per impact', () => {
    expect(conflictLines([{ key: 'REQ-1', conflicts: ['props.title', 'owner'] }, { impact: 'abc', conflicts: ['state'] }])).toEqual([
      'REQ-1: property “title”, owner',
      'abc: lifecycle state',
    ]);
  });
  it('names where the change was rebased onto', () => {
    expect(rebasedOnto({ branch: 'main', onto: 'T1T1T1T1-22' })).toBe('main (T1T1T1T1)');
    expect(rebasedOnto({ branch: 'main' })).toBe('main');
    expect(rebasedOnto({})).toBe('its target');
  });
});
