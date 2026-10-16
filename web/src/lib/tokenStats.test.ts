import { describe, expect, it } from 'vitest';
import { actorLabel, bucketsOf, bucketTime, callOf, compactTokens, directSplit, periodStart, viaOf, USAGE_PERIODS, exchangeRequestOf, hasExchange, inChangeLog, keyLabel, runsOf, slicesOf, sourceLabel, stepOf, topCalls, totalsOf } from './tokenStats';

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
    expect(sourceLabel('connector:claude-coding-agent')).toBe('Claude coding agent');
    expect(sourceLabel('connector:other-agent')).toBe('Connector other-agent');
    expect(sourceLabel(undefined)).toBe('Other');
    expect(keyLabel('source', 'helper')).toBe('Helper');
    expect(keyLabel('alias', '')).toBe('—');
  });
});

describe('prompt link', () => {
  it('is offered for engine calls of a process and a change (change log) and for the calls the gateway stores', () => {
    expect(hasExchange({ source: 'engine', processId: 'P1', changeId: 'C1' })).toBe(true);
    expect(hasExchange({ source: 'engine', processId: '', changeId: 'C1' })).toBe(false);
    expect(hasExchange({ source: 'engine', processId: 'P1', changeId: '' })).toBe(false);
    expect(hasExchange({ source: 'engine', processId: 'P1', changeId: '', hasExchange: true })).toBe(true);
    expect(hasExchange({ source: 'assistant', processId: 'P1', changeId: 'C1' })).toBe(false);
    expect(hasExchange({ source: 'assistant', hasExchange: true })).toBe(true);
    expect(hasExchange({ source: 'helper' })).toBe(false);
  });
  it('looks an exchange up in the change log only for an engine call of a change', () => {
    expect(inChangeLog({ source: 'engine', processId: 'P1', changeId: 'C1' })).toBe(true);
    expect(inChangeLog({ source: 'engine', processId: 'P1', changeId: '' })).toBe(false);
    expect(inChangeLog({ source: 'assistant', processId: 'P1', changeId: 'C1' })).toBe(false);
  });
  it('chooses the change log or the gateway to read the prompt', () => {
    const engine = { seq: 7, source: 'engine', processId: 'P1', changeId: 'C1', step: 2, call: 1, hasExchange: false };
    expect(exchangeRequestOf(engine, 'x')).toMatchObject({ changeId: 'C1', processId: 'P1', step: 2, call: 1 });
    expect(exchangeRequestOf({ seq: 8, source: 'assistant', hasExchange: true }, 'x')).toMatchObject({ seq: 8 });
    expect(exchangeRequestOf({ seq: 9, source: 'engine', processId: 'P1', changeId: '', hasExchange: true }, 'x')).toMatchObject({ seq: 9 });
    expect(exchangeRequestOf({ seq: 10, source: 'helper' }, 'x')).toBeUndefined();
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

describe('usage periods', () => {
  it('starts today at local midnight and the rolling periods from now', () => {
    const now = new Date(2026, 9, 13, 17, 30, 12).getTime();
    expect(periodStart('today', now)).toBe(new Date(2026, 9, 13).getTime());
    expect(periodStart('24h', now)).toBe(now - 86_400_000);
    expect(periodStart('7d', now)).toBe(now - 7 * 86_400_000);
    expect(periodStart('all', now)).toBe(0);
    expect(periodStart('unknown', now)).toBe(0);
    expect(USAGE_PERIODS.map(([id]) => id)).toEqual(['today', '24h', '7d', '30d', 'all']);
  });

  it('writes token counts compactly', () => {
    expect([0, 950, 12_345, 4_100_000].map(compactTokens)).toEqual(['0', '950', '12.3k', '4.1M']);
  });
});

describe('consumption on behalf of the end user (ADR 0114)', () => {
  it('names who acted', () => {
    expect(actorLabel('system:trigger:noted/writer/on_edit')).toBe('trigger noted/writer/on_edit');
    expect(actorLabel('system:companion:sdlc/description_review')).toBe('companion sdlc/description_review');
    expect(actorLabel('system:indexer')).toBe('indexer');
    expect(actorLabel('alice', 'alice')).toBe('you, directly');
    expect(actorLabel('bob', 'alice')).toBe('bob');
    expect(actorLabel('')).toBe('—');
    expect(keyLabel('subject', 'alice', 'alice')).toBe('you, directly');
  });

  it('tells a call made on the end user\'s behalf', () => {
    expect(viaOf({ subject: 'system:trigger:nightly', endUser: 'alice' })).toBe('trigger nightly');
    expect(viaOf({ subject: 'alice', endUser: 'alice' })).toBe('');
    expect(viaOf({ subject: 'system:indexer' })).toBe('');
  });

  it('splits direct and indirect consumption', () => {
    const s = directSplit(
      [
        { key: 'alice', calls: '2', inputTokens: '10', outputTokens: '5' },
        { key: 'system:trigger:nightly', calls: '1', inputTokens: '100', outputTokens: '20' },
      ],
      'alice',
    );
    expect(s.all.total).toBe(135);
    expect(s.direct.total).toBe(15);
    expect(s.indirect.total).toBe(120);
    expect(s.indirect.calls).toBe(1);
  });
});
