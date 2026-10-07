import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ listUsage: vi.fn() }));
const session = vi.hoisted(() => ({ admin: false }));
vi.mock('../api', () => ({
  models: { listUsage: api.listUsage },
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
  int: (v: unknown) => Number(v ?? 0) || 0,
}));
vi.mock('./session.svelte', () => ({
  me: () => 'alice',
  can: {
    get administer() {
      return session.admin;
    },
  },
}));

import { attachUsage, clearUsage, MAX_ROWS, PAGE, POLL_MS, resetUsage, setUsagePlatform, usage } from './usage.svelte';

const call = (seq: number) => ({ seq: String(seq), model: 'm', source: 'engine' });
const page = (from: number, to: number, more = false) => ({
  calls: Array.from({ length: Math.max(0, to - from + 1) }, (_, i) => call(from + i)),
  nextSeq: String(to),
  hasMore: more,
});
async function tick(ms = POLL_MS) {
  await vi.advanceTimersByTimeAsync(ms);
}

beforeEach(() => {
  vi.useFakeTimers();
  resetUsage();
  api.listUsage.mockReset();
  session.admin = false;
});
afterEach(() => {
  resetUsage();
  vi.useRealTimers();
});

describe('usage feed', () => {
  it('loads the latest rows of the caller, then follows by cursor', async () => {
    api.listUsage.mockResolvedValueOnce(page(1, 3)).mockResolvedValueOnce(page(4, 5)).mockResolvedValue(page(5, 5));
    const detach = attachUsage();
    await tick(0);
    expect(api.listUsage).toHaveBeenNthCalledWith(1, { subject: 'alice', afterSeq: undefined, limit: PAGE }, expect.anything());
    expect(usage.rows.map((r) => r.seq)).toEqual(['1', '2', '3']);
    await tick();
    expect(api.listUsage).toHaveBeenNthCalledWith(2, { subject: 'alice', afterSeq: '3', limit: PAGE }, expect.anything());
    expect(usage.rows.map((r) => r.seq)).toEqual(['1', '2', '3', '4', '5']);
    detach();
  });

  it('reads a backlog without waiting', async () => {
    api.listUsage.mockResolvedValueOnce(page(1, 2, true)).mockResolvedValueOnce(page(3, 4)).mockResolvedValue(page(4, 4));
    const detach = attachUsage();
    await tick(0);
    await tick(0);
    expect(usage.rows).toHaveLength(4);
    detach();
  });

  it('keeps a bounded number of rows, dropping the oldest', async () => {
    api.listUsage.mockResolvedValueOnce(page(1, MAX_ROWS)).mockResolvedValueOnce(page(MAX_ROWS + 1, MAX_ROWS + 3)).mockResolvedValue(page(1, 0));
    const detach = attachUsage();
    await tick(0);
    await tick();
    expect(usage.rows).toHaveLength(MAX_ROWS);
    expect(usage.rows[0].seq).toBe('4');
    expect(usage.rows[MAX_ROWS - 1].seq).toBe(String(MAX_ROWS + 3));
    detach();
  });

  it('clears locally: the rows are hidden and the cursor goes on', async () => {
    api.listUsage.mockResolvedValueOnce(page(1, 3)).mockResolvedValueOnce(page(1, 4)).mockResolvedValue(page(4, 4));
    const detach = attachUsage();
    await tick(0);
    clearUsage();
    expect(usage.rows).toEqual([]);
    await tick();
    // a row at or below the cleared seq does not come back
    expect(usage.rows.map((r) => r.seq)).toEqual(['4']);
    detach();
  });

  it('stops polling when detached and resumes from its cursor', async () => {
    api.listUsage.mockResolvedValue(page(1, 1));
    const detach = attachUsage();
    await tick(0);
    detach();
    const n = api.listUsage.mock.calls.length;
    await tick(10 * POLL_MS);
    expect(api.listUsage).toHaveBeenCalledTimes(n);
    api.listUsage.mockResolvedValue(page(2, 2));
    const again = attachUsage();
    await tick(0);
    expect(api.listUsage).toHaveBeenLastCalledWith({ subject: 'alice', afterSeq: '1', limit: PAGE }, expect.anything());
    expect(usage.rows.map((r) => r.seq)).toEqual(['1', '2']);
    again();
  });

  it('backs off on errors and recovers', async () => {
    api.listUsage.mockRejectedValueOnce(new Error('down')).mockResolvedValue(page(1, 1));
    const detach = attachUsage();
    await tick(0);
    expect(usage.error).toBe('down');
    await tick(POLL_MS);
    expect(api.listUsage).toHaveBeenCalledTimes(1);
    await tick(POLL_MS);
    expect(api.listUsage).toHaveBeenCalledTimes(2);
    expect(usage.error).toBe('');
    expect(usage.rows).toHaveLength(1);
    detach();
  });

  it('asks for the whole platform only as an administrator', async () => {
    api.listUsage.mockResolvedValue(page(1, 1));
    session.admin = true;
    const detach = attachUsage();
    await tick(0);
    setUsagePlatform(true);
    await tick(0);
    expect(api.listUsage).toHaveBeenLastCalledWith({ subject: '', afterSeq: undefined, limit: PAGE }, expect.anything());
    expect(usage.rows).toHaveLength(1);
    detach();
  });
});
