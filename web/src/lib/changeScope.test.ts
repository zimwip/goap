import { describe, expect, it } from 'vitest';
import { MAIN_SCOPE, scopeKind, scopeStatus, scopeTabTitle } from './changeScope';

describe('scope header', () => {
  it('names the kind of scope', () => {
    expect(scopeKind(MAIN_SCOPE)).toBe('Flow');
    expect(scopeKind('')).toBe('Flow');
    expect(scopeKind('f1')).toBe('Option');
  });
  it('states what the scope shows', () => {
    expect(scopeStatus(MAIN_SCOPE, true, false)).toBe('the change as agreed');
    expect(scopeStatus('f1', true, false)).toContain('the main flow, and what it changes');
    expect(scopeStatus('f1', false, false)).toContain('read-only: the option is decided');
    expect(scopeStatus('f1', false, true)).not.toContain('read-only');
  });
  it('titles the scoped tabs', () => {
    expect(scopeTabTitle([], MAIN_SCOPE)).toBe('Shows Main flow');
    expect(scopeTabTitle([{ id: 'f1', option: { name: 'Plan B' } } as never], 'f1')).toBe('Shows Plan B');
  });
});
