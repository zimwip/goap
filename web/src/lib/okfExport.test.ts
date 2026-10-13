import { describe, expect, it } from 'vitest';
import { fromBase64, okfCollector } from './okfExport';

describe('okfCollector', () => {
  it('takes the name from the first message and the bytes from the next ones, in order', () => {
    const { archive, take } = okfCollector('fallback.zip');
    take({ filename: 'graph-alm.okf.zip', mediaType: 'application/zip', nodes: 3 });
    take({ chunk: btoa('PK\x03') });
    take({ chunk: btoa('\x04') });
    expect(archive.filename).toBe('graph-alm.okf.zip');
    expect(archive.nodes).toBe(3);
    expect(archive.parts.map((p) => [...p])).toEqual([[0x50, 0x4b, 3], [4]]);
  });

  it('keeps the fallback name when the stream names none', () => {
    const { archive } = okfCollector('fallback.zip');
    expect(archive.filename).toBe('fallback.zip');
    expect([...fromBase64('')]).toEqual([]);
  });
});
