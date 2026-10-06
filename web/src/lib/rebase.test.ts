import { describe, expect, it } from 'vitest';
import { conflictLabel } from './rebase';

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
