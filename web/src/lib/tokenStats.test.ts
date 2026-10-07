import { describe, expect, it } from 'vitest';
import { bucketsOf, bucketTime, callOf, hasExchange, keyLabel, runsOf, slicesOf, sourceLabel, stepOf, topCalls, totalsOf } from './tokenStats';

describe('source labels', () => {
  it('names every source and falls back to Other', () => {
    expect(['assistant', 'helper', 'engine', 'indexer', 'intent', 'embed', 'other'].map(sourceLabel)).toEqual([
      'Assistant',
      'Helper',
      'Engine',
      'Indexer',
      'Intent',
      'Embeddings',
      'Other',
    ]);
    expect(sourceLabel('mystery')).toBe('Other');
    expect(sourceLabel(undefined)).toBe('Other');
    expect(keyLabel('source', 'helper')).toBe('Helper');
    expect(keyLabel('alias', '')).toBe('—');
  });
});

describe('prompt link', () => {
  it('is offered for engine calls of a process only', () => {
    expect(hasExchange({ source: 'engine', processId: 'P1', changeId: 'C1' })).toBe(true);
    expect(hasExchange({ source: 'engine', processId: '', changeId: 'C1' })).toBe(false);
    expect(hasExchange({ source: 'engine', processId: 'P1', changeId: '' })).toBe(false);
    expect(hasExchange({ source: 'assistant', processId: 'P1', changeId: 'C1' })).toBe(false);
    expect(hasExchange({ source: 'helper' })).toBe(false);
  });
  it('reads the step and call of a process call, -1 without one (proto3 omits a 0)', () => {
    expect(stepOf({ processId: 'P1' })).toBe(0);
    expect(callOf({ processId: 'P1', call: 2 })).toBe(2);
    expect(stepOf({ processId: '', step: -1 })).toBe(-1);
  });
});

describe('summary mapping', () => {
  const rows = [
    { key: 'a', calls: '2', inputTokens: '10', outputTokens: '5', errors: '1', durationMs: '30' },
    { key: 'engine', calls: '3', inputTokens: '100', outputTokens: '50' },
    { key: '', calls: '1', inputTokens: '1', outputTokens: '1' },
  ];
  it('maps int64 strings and sorts by tokens', () => {
    const s = slicesOf('source', rows);
    expect(s.map((x) => [x.label, x.total, x.calls])).toEqual([['Engine', 150, 3], ['Other', 15, 2], ['Other', 2, 1]]);
  });
  it('totals a grouping', () => {
    expect(totalsOf(rows)).toEqual({ input: 111, output: 56, total: 167, calls: 6, errors: 1, durationMs: 30 });
    expect(totalsOf(undefined).total).toBe(0);
  });
  it('lists runs with their ratio to the average, leaving out calls of no process', () => {
    const { runs, avg } = runsOf([
      { key: 'P1', inputTokens: '90', outputTokens: '10' },
      { key: 'P2', inputTokens: '10', outputTokens: '10' },
      { key: '', inputTokens: '999', outputTokens: '1' },
    ]);
    expect(runs.map((r) => r.key)).toEqual(['P1', 'P2']);
    expect(avg).toBe(60);
    expect(runs[0].ratio).toBeCloseTo(100 / 60);
  });
  it('keeps the most expensive calls', () => {
    const calls = [{ seq: '1', inputTokens: '5' }, { seq: '2', inputTokens: '50', outputTokens: '5' }, { seq: '3', outputTokens: '9' }];
    expect(topCalls(calls, 2).map((c) => c.seq)).toEqual(['2', '3']);
  });
});

describe('time buckets', () => {
  it('parses the UTC keys of the ledger', () => {
    expect(bucketTime('2026-10-07')).toBe(Date.UTC(2026, 9, 7));
    expect(bucketTime('2026-10-07T13')).toBe(Date.UTC(2026, 9, 7, 13));
  });
  it('fills the empty days between the rows and the end', () => {
    const rows = [
      { key: '2026-10-05', inputTokens: '10', outputTokens: '2', calls: '1' },
      { key: '2026-10-07', inputTokens: '4', outputTokens: '1', calls: '2' },
    ];
    const b = bucketsOf(rows, false, 0, Date.UTC(2026, 9, 8, 3));
    expect(b.map((x) => [x.key, x.input])).toEqual([['2026-10-05', 10], ['2026-10-06', 0], ['2026-10-07', 4], ['2026-10-08', 0]]);
  });
  it('fills hours from the start of the period', () => {
    const b = bucketsOf([{ key: '2026-10-07T01', inputTokens: '3' }], true, Date.UTC(2026, 9, 7, 0, 30), Date.UTC(2026, 9, 7, 2, 10));
    expect(b.map((x) => [x.label, x.input])).toEqual([['00:00', 0], ['01:00', 3], ['02:00', 0]]);
  });
  it('is empty with no data and no start', () => {
    expect(bucketsOf([], false, 0, Date.now())).toEqual([]);
  });
});
