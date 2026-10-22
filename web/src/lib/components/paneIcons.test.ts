import { describe, expect, it } from 'vitest';
import { paneIcon } from './paneIcons';

describe('paneIcon', () => {
  it('takes the icon of the pane, else the one of its id, else a list', () => {
    expect(paneIcon({ id: 'impacts' })).toBe('diff');
    expect(paneIcon({ id: 'agents', icon: 'bot' })).toBe('bot');
    expect(paneIcon({ id: 'unknown' })).toBe('list');
  });
});
