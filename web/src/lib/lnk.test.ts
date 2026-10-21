import { describe, expect, it } from 'vitest';
import { parseLnk, refKey } from './lnk';

describe('lnk', () => {
  it('reads a logical URL like lnk.Parse', () => {
    expect(parseLnk('lnk:organisation/OrgUnit/ORG-DEFAULT')).toEqual({ source: 'organisation', type: 'OrgUnit', key: 'ORG-DEFAULT' });
    expect(parseLnk('lnk:code/File/src/main%20x.go?v=3')).toEqual({ source: 'code', type: 'File', key: 'src/main x.go', version: 3 });
    expect(parseLnk('ORG-DEFAULT')).toBeUndefined();
    expect(parseLnk('lnk:organisation/OrgUnit')).toBeUndefined();
  });

  it('gives the key of a reference, a bare key as is', () => {
    expect(refKey('lnk:organisation/ProjectUnit/PROJ-ROOT')).toBe('PROJ-ROOT');
    expect(refKey('PROJ-ROOT')).toBe('PROJ-ROOT');
    expect(refKey(undefined)).toBe('');
  });
});
