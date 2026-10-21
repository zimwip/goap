import { describe, expect, it } from 'vitest';
import { compactBadge, paneIcon } from './context.svelte';

describe('context bar', () => {
  it('shows the number of a badge on a closed icon', () => {
    expect(compactBadge(undefined)).toBe('');
    expect(compactBadge('')).toBe('');
    expect(compactBadge(3)).toBe('3');
    expect(compactBadge(120)).toBe('99+');
    expect(compactBadge('4 to review')).toBe('4');
    expect(compactBadge('!')).toBe('!');
    expect(compactBadge('alm')).toBe('');
  });
  it('gives a section the icon of its id unless it names one', () => {
    expect(paneIcon({ id: 'impacts', label: 'Impacts' })).toBe('diff');
    expect(paneIcon({ id: 'agents', label: 'Agents', icon: 'bot' })).toBe('bot');
    expect(paneIcon({ id: 'unknown', label: 'Other' })).toBe('list');
  });
});
