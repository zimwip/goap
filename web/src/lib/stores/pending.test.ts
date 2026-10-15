import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ stage: vi.fn(), get: vi.fn(), save: vi.fn(), discard: vi.fn() }));
vi.mock('../api', () => ({
  graph: { stageSettings: api.stage, getPendingSettings: api.get, saveSettings: api.save, discardSettings: api.discard },
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));
vi.mock('./session.svelte', () => ({ me: () => 'alice' }));

import { discardPending, loadPending, pending, pendingCount, savePending, stageEdits, stageRetire, stageUpsert, stagedNode } from './pending.svelte';

const state = (nodes: object[]) => ({ changes: [{ namespace: 'platform', changeId: 'CH1', nodes }] });
const p1 = { key: 'LLP:p1', type: 'platform@LlmProvider', props: { name: 'p1' }, created: true, changeImpactId: 'I1', written: true };

beforeEach(() => {
  for (const f of Object.values(api)) f.mockReset();
  pending.byNs = {};
  pending.error = '';
});

describe('pending settings', () => {
  it('stages an edit in one call and holds the state the server answers', async () => {
    api.stage.mockResolvedValue(state([p1]));
    await stageUpsert('platform', 'platform@LlmProvider', 'LLP:p1', { name: 'p1' });
    expect(api.stage).toHaveBeenCalledTimes(1);
    expect(api.stage).toHaveBeenCalledWith([{ namespace: 'platform', type: 'platform@LlmProvider', key: 'LLP:p1', props: { name: 'p1' } }]);
    expect(stagedNode('platform', 'LLP:p1')).toMatchObject({ created: true, written: true, retire: false, impactId: 'I1', props: { name: 'p1' } });
    expect(pendingCount()).toBe(1);
  });

  it('sends a batch of retirements as one call', async () => {
    api.stage.mockResolvedValue({});
    await stageEdits([
      { namespace: 'platform', type: 'platform@LlmAlias', key: 'LLA:a', retire: true },
      { namespace: 'platform', type: 'platform@LlmModel', key: 'LLM:p/m', retire: true },
    ]);
    expect(api.stage).toHaveBeenCalledTimes(1);
    expect(pendingCount()).toBe(0);
  });

  it('shows what the server holds after a refused edit', async () => {
    api.stage.mockRejectedValue(new Error('refused'));
    api.get.mockResolvedValue(state([p1]));
    await expect(stageRetire('platform', 'platform@LlmProvider', 'LLP:x')).rejects.toThrow('refused');
    expect(pending.error).toBe('refused');
    expect(stagedNode('platform', 'LLP:p1')).toBeDefined();
  });

  it('loads, saves and discards in one call each', async () => {
    api.get.mockResolvedValue(state([p1]));
    await loadPending();
    expect(pendingCount()).toBe(1);
    api.save.mockResolvedValue({});
    expect(await savePending()).toBe(true);
    expect(pendingCount()).toBe(0);
    api.get.mockResolvedValue(state([p1]));
    await loadPending();
    api.discard.mockResolvedValue({});
    expect(await discardPending()).toBe(true);
    expect(api.save).toHaveBeenCalledTimes(1);
    expect(api.discard).toHaveBeenCalledTimes(1);
    expect(pending.byNs).toEqual({});
  });
});
