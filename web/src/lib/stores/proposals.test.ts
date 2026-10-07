import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ confirm: vi.fn() }));
const store = vi.hoisted(() => ({ applyMessage: vi.fn(), openConversation: vi.fn(async () => {}), reportOutcome: vi.fn(), assistant: { currentId: 'C1' } }));
class RpcErr extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
  }
}
vi.mock('../api', () => ({
  assistantApi: { confirmAction: api.confirm },
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));
vi.mock('./assistant.svelte', () => store);
vi.mock('./project.svelte', () => ({ project: { current: 'PROJ-A' }, selectProject: vi.fn(), followChangeProject: vi.fn() }));
vi.mock('./catalog.svelte', () => ({ changes: { items: [] }, refreshChanges: vi.fn() }));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => undefined, openTab: vi.fn() }));
vi.mock('../shell/workbench.svelte', () => ({ notify: vi.fn() }));

import { decide, proposalKey, proposals, resetProposals, retryApplied, STALE_NOTE } from './proposals.svelte';
import { registerAssist, resetRegistry } from '../assist/registry.svelte';
import type { ConversationMessage } from '../api';

const msg = (action: { type: string }, id = 'A2'): ConversationMessage => ({ id, conversationId: 'C1', seq: 2, role: 'assistant', status: 'done', actions: [action] });
const write = (over = {}) => ({ type: 'ui_tool' as const, status: 'proposed', level: 'write', tool: 'rename_change', args: { title: 'New' }, label: 'Rename', project: 'PROJ-A', ...over });
const agent = (over = {}) => ({ type: 'start_agent' as const, status: 'proposed', label: 'Run', args: { methodology: 'm', agent: 'a', project: 'PROJ-A' }, ...over });

beforeEach(() => {
  resetRegistry();
  resetProposals();
  Object.values(api).forEach((f) => f.mockReset());
  store.applyMessage.mockReset();
  store.openConversation.mockReset();
  store.reportOutcome.mockReset();
});

describe('deciding a write proposal', () => {
  it('runs nothing before the server accepted, then runs the tool and reports done', async () => {
    const order: string[] = [];
    registerAssist({ tools: [{ name: 'rename_change', run: (a) => void order.push(`run ${a.title}`) }] });
    const m = msg(write());
    api.confirm.mockImplementation(async () => (order.push('confirm'), { message: msg(write({ status: 'accepted' })) }));
    store.reportOutcome.mockImplementation(async (_m: unknown, _i: number, status: string) => void order.push(`report ${status}`));
    await decide(m, 0, 'accept');
    expect(api.confirm).toHaveBeenCalledWith({ conversationId: 'C1', messageId: 'A2', actionIndex: 0, decision: 'accept', project: 'PROJ-A' });
    expect(order).toEqual(['confirm', 'run New', 'report done']);
    expect(store.applyMessage).toHaveBeenCalled();
    expect(proposals.busy[proposalKey(m, 0)]).toBeUndefined();
  });

  it('reports failed with the error when the tool refuses or is no longer registered', async () => {
    registerAssist({ tools: [{ name: 'rename_change', run: () => 'The title is taken.' }] });
    api.confirm.mockResolvedValue({ message: msg(write({ status: 'accepted' })) });
    await decide(msg(write()), 0, 'accept');
    expect(store.reportOutcome).toHaveBeenLastCalledWith(expect.anything(), 0, 'failed', 'The title is taken.');

    resetRegistry();
    await decide(msg(write()), 0, 'accept');
    expect(store.reportOutcome).toHaveBeenLastCalledWith(expect.anything(), 0, 'failed', expect.stringContaining('no longer offers'));
  });

  it('a reject runs nothing', async () => {
    const run = vi.fn();
    registerAssist({ tools: [{ name: 'rename_change', run }] });
    api.confirm.mockResolvedValue({ message: msg(write({ status: 'rejected' })) });
    await decide(msg(write()), 0, 'reject');
    expect(api.confirm).toHaveBeenCalledWith(expect.objectContaining({ decision: 'reject' }));
    expect(run).not.toHaveBeenCalled();
    expect(store.reportOutcome).not.toHaveBeenCalled();
  });

  it('says a stale proposal is no longer valid and can be rejected, and runs nothing', async () => {
    const run = vi.fn();
    registerAssist({ tools: [{ name: 'rename_change', run }] });
    api.confirm.mockRejectedValue(new RpcErr('failed_precondition', 'stale'));
    const m = msg(write());
    await decide(m, 0, 'accept');
    expect(proposals.notes[proposalKey(m, 0)]).toBe(STALE_NOTE);
    expect(run).not.toHaveBeenCalled();
    expect(proposals.busy[proposalKey(m, 0)]).toBeUndefined();
  });

  it('reads the conversation again when the proposal was already decided', async () => {
    api.confirm.mockRejectedValue(new RpcErr('aborted', 'decided'));
    await decide(msg(write()), 0, 'accept');
    expect(store.openConversation).toHaveBeenCalledWith('C1', true);
  });

  it('shows any other error', async () => {
    api.confirm.mockRejectedValue(new Error('network down'));
    const m = msg(write());
    await decide(m, 0, 'accept');
    expect(proposals.notes[proposalKey(m, 0)]).toBe('network down');
  });

  it('does not decide twice at once', async () => {
    let release: (v: unknown) => void = () => {};
    api.confirm.mockReturnValue(new Promise((r) => (release = r)));
    const m = msg(write());
    const first = decide(m, 0, 'reject');
    await decide(m, 0, 'accept');
    expect(api.confirm).toHaveBeenCalledTimes(1);
    release({ message: msg(write({ status: 'rejected' })) });
    await first;
  });

  it('a report refused because it was already recorded reads the conversation again', async () => {
    registerAssist({ tools: [{ name: 'rename_change', run: () => {} }] });
    api.confirm.mockResolvedValue({ message: msg(write({ status: 'accepted' })) });
    store.reportOutcome.mockRejectedValue(new RpcErr('aborted', 'reported'));
    await decide(msg(write()), 0, 'accept');
    expect(store.openConversation).toHaveBeenCalled();
  });
});

describe('deciding a start_agent proposal', () => {
  it('accepts through the server only: nothing runs on the screen, nothing is reported', async () => {
    api.confirm.mockResolvedValue({ message: msg(agent({ status: 'started', result: { processId: 'P1' } })) });
    await decide(msg(agent()), 0, 'accept');
    expect(api.confirm).toHaveBeenCalledWith(expect.objectContaining({ decision: 'accept', project: 'PROJ-A' }));
    expect(store.applyMessage).toHaveBeenCalledWith(expect.objectContaining({ id: 'A2' }));
    expect(store.reportOutcome).not.toHaveBeenCalled();
  });
});

describe('an accepted write found after a reload', () => {
  const accepted = () => msg(write({ status: 'accepted' }));

  it('is retried only when the tool is registered now, then reports', async () => {
    const run = vi.fn();
    await retryApplied(accepted(), 0, false);
    expect(proposals.notes[proposalKey(accepted(), 0)]).toMatch(/not open/);
    expect(run).not.toHaveBeenCalled();
    expect(store.reportOutcome).not.toHaveBeenCalled();

    registerAssist({ tools: [{ name: 'rename_change', run }] });
    await retryApplied(accepted(), 0, true);
    expect(run).toHaveBeenCalledWith({ title: 'New' });
    expect(store.reportOutcome).toHaveBeenCalledWith(expect.anything(), 0, 'done', undefined);
  });

  it('is not retried when it was not accepted', async () => {
    const run = vi.fn();
    registerAssist({ tools: [{ name: 'rename_change', run }] });
    await retryApplied(msg(write({ status: 'proposed' })), 0, true);
    await retryApplied(msg(write({ status: 'done' })), 0, true);
    expect(run).not.toHaveBeenCalled();
  });
});
