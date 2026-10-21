import { describe, expect, it } from 'vitest';
import { elapsed, fraction, graphImportResult, isActive, isFinal, jobChange, jobFacts, kindLabel, newer, paramLines, statsLine } from './jobView';

describe('jobs', () => {
  it('names the kinds', () => {
    expect(kindLabel('import.code.git')).toBe('Import code (git)');
    expect(kindLabel('graph.export')).toBe('Graph export');
    expect(kindLabel('other.kind')).toBe('other.kind');
  });

  it('measures the progress', () => {
    expect(fraction({ done: '3', total: '4' })).toBe(0.75);
    expect(fraction({ status: 'running' })).toBeUndefined();
    expect(fraction({ status: 'done' })).toBe(1);
    expect(isActive({ status: 'queued' })).toBe(true);
    expect(isFinal({ status: 'cancelled' })).toBe(true);
    expect(statsLine({ stats: { commits: '12', created: '40', removed: '0' } })).toBe('12 commits, 40 created');
    const t = Date.parse('2026-10-20T10:00:00Z');
    expect(elapsed({ startedAt: '2026-10-20T10:00:00Z' }, t + 65_000)).toBe('1 min 5 s');
    expect(elapsed({ startedAt: '2026-10-20T10:00:00Z', endedAt: '2026-10-20T10:00:12Z' }, t + 99_000)).toBe('12 s');
    expect(elapsed({ status: 'queued' }, t)).toBe('');
  });

  it('lists what the detail shows', () => {
    const t = Date.parse('2026-10-20T10:00:00Z');
    const facts = jobFacts({ id: 'j1', kind: 'graph.export', by: 'ann', startedAt: '2026-10-20T10:00:00Z' }, t + 5_000);
    expect(facts.map((f) => f.label)).toEqual(['Kind', 'Started by', 'Started', 'Duration', 'Id']);
    expect(facts.find((f) => f.label === 'Duration')?.value).toBe('5 s');
    expect(paramLines({ params: { directory: '/src/jq', depth: 2, empty: '', opts: { a: 1 } } })).toEqual([
      { name: 'directory', value: '/src/jq' },
      { name: 'depth', value: '2' },
      { name: 'opts', value: '{"a":1}' },
    ]);
  });

  it('never takes an ended job back', () => {
    const done = { id: 'a', status: 'done', updatedAt: '2026-10-20T10:00:02Z' };
    expect(newer(done, { id: 'a', status: 'running', updatedAt: '2026-10-20T10:00:03Z' })).toBe(false);
    expect(newer({ id: 'a', status: 'running', updatedAt: '2026-10-20T10:00:02Z' }, { id: 'a', status: 'running', updatedAt: '2026-10-20T10:00:01.5Z' })).toBe(false);
    expect(newer({ id: 'a', status: 'queued', updatedAt: '2026-10-20T10:00:00Z' }, done)).toBe(true);
  });

  it('reads what a graph import did', () => {
    const j = { status: 'done', output: { created: 2, updated: 1, unchanged: 5, warnings: ['w'], changeId: 'C1', changeTitle: 'Import' } };
    expect(jobChange(j)).toEqual({ id: 'C1', title: 'Import' });
    expect(graphImportResult(j)).toEqual({ change: { id: 'C1', title: 'Import' }, created: 2, updated: 1, unchanged: 5, warnings: ['w'] });
    expect(graphImportResult({ output: { unchanged: 3 } }).change).toBeUndefined();
  });
});

describe('job feedback', () => {
  it('estimates what is left and reads the log', async () => {
    const { remaining, logLines } = await import('./jobView');
    const start = Date.parse('2026-10-20T10:00:00Z');
    const j = { status: 'running', startedAt: '2026-10-20T10:00:00Z', done: '10', total: '40' };
    expect(remaining(j, start + 20_000)).toBe('about 1 min 0 s left');
    expect(remaining(j, start + 1000)).toBe('');
    expect(remaining({ ...j, status: 'done' }, start + 20_000)).toBe('');
    expect(remaining({ ...j, total: '0' }, start + 20_000)).toBe('');
    expect(logLines({ log: [{ at: '2026-10-20T10:00:01Z', text: 'commit 1/40 abc first' }] })[0].text).toBe('commit 1/40 abc first');
  });
});
