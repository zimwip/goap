import { describe, expect, it } from 'vitest';
import { sseParser } from './jobs';

describe('job stream', () => {
  it('reads server-sent events however the text is cut', () => {
    const got: [string, string][] = [];
    const feed = sseParser((e, d) => got.push([e, d]));
    feed('retry: 3000\nevent: snap');
    feed('shot\ndata: {"jobs":[]}\n');
    expect(got).toEqual([]);
    feed('\n: ping\n\nevent: job\r\ndata: {"id":"a",\r\ndata: "status":"done"}\r\n\r\ndata: plain\n\n');
    expect(got).toEqual([
      ['snapshot', '{"jobs":[]}'],
      ['job', '{"id":"a",\n"status":"done"}'],
      ['message', 'plain'],
    ]);
  });
});
