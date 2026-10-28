import { describe, expect, it } from 'vitest';
import { isAddress, openingOf, schemeOf } from './address';

describe('address', () => {
  it('reads addresses', () => {
    expect(isAddress('lnk:alm/Requirement/R-1')).toBe(true);
    expect(isAddress('https://example.com/a')).toBe(true);
    expect(isAddress('plain text')).toBe(false);
    expect(isAddress(3)).toBe(false);
    expect(schemeOf(' HTTPS://x')).toBe('https');
  });

  it('opens what the source says', () => {
    expect(openingOf({ type: 'alm@Requirement', key: 'R-1', node: { id: 'n1' }, open: { mode: 'node' } })).toEqual({
      kind: 'node',
      node: { id: 'n1', key: 'R-1', type: 'alm@Requirement', namespace: 'alm' },
    });
    expect(openingOf({ open: { mode: 'external', href: 'https://example.com/a.png' } })).toEqual({ kind: 'tab', href: 'https://example.com/a.png' });
    expect(openingOf({ open: { mode: 'external', href: 'javascript:alert(1)' } })).toBeUndefined();
    expect(openingOf({ title: 'a.go', content: 'package a', open: { mode: 'text', language: 'go' } })).toMatchObject({ kind: 'text', language: 'go', content: 'package a' });
    expect(openingOf({ title: 'x.svg', open: { mode: 'inline', href: 'data:text/html;base64,AA' } })).toBeUndefined();
    expect(openingOf({ title: 'logo.png', open: { mode: 'inline', href: 'data:image/png;base64,AA', media: 'image/png' } })?.kind).toBe('image');
    expect(openingOf({ title: 'blob', location: '/srv/x/blob', open: { mode: 'path' } })).toMatchObject({ kind: 'path', href: '/srv/x/blob' });
    expect(openingOf({ url: 'lnk:a/B/c' })).toBeUndefined();
  });
});
