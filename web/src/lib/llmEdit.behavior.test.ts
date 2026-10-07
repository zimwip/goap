import { beforeEach, describe, expect, it, vi } from 'vitest';

const staged: { type: string; key: string; props: Record<string, unknown>; retire?: boolean }[] = [];
const stageUpsert = vi.fn(async () => {});
const stageRetire = vi.fn(async () => {});

vi.mock('./api', () => ({ graph: {}, isDraft: () => false }));
vi.mock('./graphEdit', () => ({ headGraph: vi.fn() }));
vi.mock('./stores/session.svelte', () => ({ ns: { platform: 'platform' }, isUserKey: () => false }));
vi.mock('./stores/pending.svelte', () => ({
  pending: { byNs: {} },
  stageUpsert: (...a: unknown[]) => stageUpsert(...(a as [])),
  stageRetire: (...a: unknown[]) => stageRetire(...(a as [])),
  stagedOfType: (_ns: string, type: string) => staged.filter((s) => s.type === type),
}));

import { BEHAVIOR_TYPE, behaviorKey, behaviorProps, overlayBehaviors, retireBehavior, saveBehavior, setBehaviorEnabled } from './llmEdit';

beforeEach(() => {
  staged.length = 0;
  stageUpsert.mockClear();
  stageRetire.mockClear();
});

describe('behaviour edits', () => {
  it('sends every key so that an emptied scope clears the stored one', () => {
    const p = behaviorProps({ name: 'terse', instruction: 'x', enabled: true, position: 'weird', appliesToJson: true });
    expect(p).toMatchObject({ name: 'terse', enabled: true, position: 'append', aliases: [], models: [], sources: [], kinds: [], appliesToJSON: true, order: 0 });
  });

  it('stages a save, a toggle and a retirement on the node of the behaviour', async () => {
    await saveBehavior({ name: 'terse', instruction: 'x' });
    expect(stageUpsert).toHaveBeenCalledWith('platform', BEHAVIOR_TYPE, 'LLB:terse', expect.objectContaining({ name: 'terse' }));
    await setBehaviorEnabled('terse', true);
    expect(stageUpsert).toHaveBeenLastCalledWith('platform', BEHAVIOR_TYPE, 'LLB:terse', { enabled: true });
    await retireBehavior('terse');
    expect(stageRetire).toHaveBeenCalledWith('platform', BEHAVIOR_TYPE, 'LLB:terse');
    expect(behaviorKey('a')).toBe('LLB:a');
  });

  it('lays the staged edits over the applied behaviours', () => {
    const applied = [
      { name: 'terse', instruction: 'x', enabled: false, aliases: ['fast'] },
      { name: 'gone', instruction: 'y', enabled: true },
    ];
    staged.push({ type: BEHAVIOR_TYPE, key: 'LLB:terse', props: { enabled: true } });
    staged.push({ type: BEHAVIOR_TYPE, key: 'LLB:gone', props: {}, retire: true });
    staged.push({ type: BEHAVIOR_TYPE, key: 'LLB:new', props: { name: 'new', instruction: 'z', sources: ['helper'] } });
    const rows = overlayBehaviors(applied);
    expect(rows.map((r) => r.name).sort()).toEqual(['new', 'terse']);
    const terse = rows.find((r) => r.name === 'terse')!;
    expect(terse).toMatchObject({ enabled: true, aliases: ['fast'], instruction: 'x', pending: true });
    expect(rows.find((r) => r.name === 'new')).toMatchObject({ sources: ['helper'], enabled: false, pending: true });
  });
});
