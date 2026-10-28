import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ getBranch: vi.fn(), getCommitGraph: vi.fn() }));
const events = vi.hoisted(() => ({ stream: { status: 'open' } }));
vi.mock('./api', () => ({ graph: api }));
vi.mock('./flux/events.svelte', () => events);

import { forgetHeads, headGraph } from './graphEdit';
import { noteCommand } from './flux/commands';
import { keyOf, touch, touchAll } from './flux/signals.svelte';

beforeEach(() => {
  forgetHeads();
  events.stream.status = 'open';
  api.getBranch.mockReset().mockResolvedValue({ head: { id: 'B1' } });
  api.getCommitGraph.mockReset().mockResolvedValue({ nodes: [{ id: 'N1', key: 'ORG-DEFAULT' }], links: [] });
});

describe('headGraph', () => {
  it('serves every reader with one read until an event moves the namespace', async () => {
    const [a, b] = await Promise.all([headGraph('organisation'), headGraph('organisation')]);
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(1);
    // each caller has its own copy
    a.nodes.push({ id: 'X' });
    expect(b.nodes).toHaveLength(1);
    touch(keyOf.namespace('organisation'));
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(2);
    touch(keyOf.commits);
    await headGraph('organisation');
    touchAll();
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(4);
  });
  it('reads again after a write of this client, not after a read', async () => {
    await headGraph('organisation');
    noteCommand('GetNode');
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(1);
    noteCommand('CommitEdits');
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(2);
  });
  it('reads each time while the stream is down, and after a failed read', async () => {
    events.stream.status = 'reconnecting';
    await headGraph('organisation');
    await headGraph('organisation');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(2);
    events.stream.status = 'open';
    api.getCommitGraph.mockRejectedValueOnce(new Error('down'));
    await expect(headGraph('platform')).rejects.toThrow('down');
    await headGraph('platform');
    expect(api.getCommitGraph).toHaveBeenCalledTimes(4);
  });
});
