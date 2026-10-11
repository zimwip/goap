import { describe, expect, it } from 'vitest';
import { changeOfBranch, shownBranches } from './branchFilter';

const b = (name: string, status: string, origin?: string) => ({ name, status, origin });

describe('shownBranches', () => {
  const all = [b('change-ab12cd34', 'merged', 'change:ab12cd34-x'), b('main', 'open'), b('flow-1', 'open'), b('dev', 'abandoned'), b('feat', 'open')];

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
