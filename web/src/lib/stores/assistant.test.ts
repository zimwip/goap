import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  create: vi.fn(),
  list: vi.fn(),
  get: vi.fn(),
  rename: vi.fn(),
  remove: vi.fn(),
  send: vi.fn(),
  report: vi.fn(),
}));
const runActions = vi.hoisted(() => vi.fn());
vi.mock('../api', () => ({
  conversationsApi: { create: api.create, list: api.list, get: api.get, rename: api.rename, remove: api.remove },
  assistantApi: { send: api.send, reportAction: api.report },
  errorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}));
vi.mock('../shell/tabs.svelte', () => ({ activeTab: () => ({ id: 'change:C1', kind: 'change', params: { id: 'C1' }, pinned: true }) }));
vi.mock('./project.svelte', () => ({ project: { current: 'PROJ-A' } }));
vi.mock('../assistant/actions', () => ({ runActions }));

import { assistant, attachView, deleteConversation, isPending, newConversation, openConversation, pollDelay, POLL_FAST_COUNT, POLL_FAST_MS, POLL_MS, resetAssistant, retry, send } from './assistant.svelte';

const user = (seq: number, text = 'hi') => ({ id: `U${seq}`, conversationId: 'C1', seq, role: 'user' as const, text, status: 'done' as const });
const bot = (seq: number, status: 'pending' | 'done' | 'error', extra = {}) => ({ id: `A${seq}`, conversationId: 'C1', seq, role: 'assistant' as const, status, ...extra });
const conv = { id: 'C1', subject: 's', title: 'hi' };
const act = { type: 'open_change', args: { changeId: 'CHG-1' } };

async function tick(ms = POLL_MS) {
  await vi.advanceTimersByTimeAsync(ms);
}

async function flush() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

beforeEach(() => {
  vi.useFakeTimers();
  resetAssistant();
  Object.values(api).forEach((f) => f.mockReset());
  runActions.mockReset();
  api.list.mockResolvedValue({ conversations: [] });
});

afterEach(() => {
  resetAssistant();
  vi.useRealTimers();
});

describe('assistant store', () => {
  it('opens the most recent conversation when first attached', async () => {
    api.list.mockResolvedValue({ conversations: [conv, { ...conv, id: 'C0' }] });
    api.get.mockResolvedValue({ conversation: conv, messages: [user(1), bot(2, 'done', { text: 'ok' })] });
    const detach = attachView();
    await flush();
    expect(assistant.currentId).toBe('C1');
    expect(assistant.messages.map((m) => m.id)).toEqual(['U1', 'A2']);
    detach();
  });

  it('creates the conversation with the first message and sends the context of the page', async () => {
    api.create.mockResolvedValue({ conversation: conv });
    api.send.mockResolvedValue({ userMessage: user(1), assistantMessage: bot(2, 'pending') });
    const detach = attachView();
    expect(await send('  hello  ')).toBe(true);
    expect(api.create).toHaveBeenCalledWith('hello');
    // the layers of the page and the tools the screen offers (none registered here)
    expect(api.send).toHaveBeenCalledWith(
      'C1',
      'hello',
      { app: { tab: { kind: 'change', params: { id: 'C1' } }, project: 'PROJ-A' }, screen: { kind: 'change' } },
      [],
    );
    expect(isPending()).toBe(true);
    expect(assistant.conversations[0].id).toBe('C1');
    detach();
  });

  it('polls while pending and stops when done, running the actions once', async () => {
    api.create.mockResolvedValue({ conversation: conv });
    api.send.mockResolvedValue({ userMessage: user(1), assistantMessage: bot(2, 'pending') });
    api.get
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'pending')] })
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'done', { text: 'there', actions: [act] })] });
    const detach = attachView();
    await send('open it');
    await tick(POLL_FAST_MS);
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(runActions).not.toHaveBeenCalled();
    await tick(POLL_FAST_MS);
    expect(api.get).toHaveBeenCalledTimes(2);
    expect(runActions).toHaveBeenCalledTimes(1);
    expect(runActions).toHaveBeenCalledWith(expect.objectContaining({ id: 'A2', actions: [act] }), expect.any(Function));
    await tick(10 * POLL_MS);
    expect(api.get).toHaveBeenCalledTimes(2);
    detach();
  });

  it('stops on error status and does not run actions', async () => {
    api.create.mockResolvedValue({ conversation: conv });
    api.send.mockResolvedValue({ userMessage: user(1), assistantMessage: bot(2, 'pending') });
    api.get.mockResolvedValue({ conversation: conv, messages: [user(1), bot(2, 'error', { error: 'boom', actions: [act] })] });
    const detach = attachView();
    await send('x');
    await tick();
    await tick(5 * POLL_MS);
    expect(api.get).toHaveBeenCalledTimes(1);
    expect(runActions).not.toHaveBeenCalled();
    detach();
  });

  it('does not replay the actions of a reloaded conversation', async () => {
    api.get.mockResolvedValue({ conversation: conv, messages: [user(1), bot(2, 'done', { actions: [act] })] });
    await openConversation('C1');
    await openConversation('C1', true);
    expect(runActions).not.toHaveBeenCalled();
  });

  it('runs the actions of a message found pending that turns done', async () => {
    api.get
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'pending')] })
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'done', { actions: [act] })] });
    const detach = attachView();
    await openConversation('C1');
    await tick();
    expect(runActions).toHaveBeenCalledTimes(1);
    detach();
  });

  it('reports the outcome of an effect screen tool and takes the updated message', async () => {
    const effect = { type: 'ui_tool', status: 'requested', level: 'effect', tool: 'select_impact', args: { impactId: 'I1' }, label: 'Select I1' };
    runActions.mockImplementation(async (m: { actions?: unknown[] }, report: (i: number, s: string, e?: string) => Promise<void>) => {
      await report(0, 'done');
      expect(m.actions).toHaveLength(1);
    });
    api.report.mockResolvedValue({ message: bot(2, 'done', { actions: [{ ...effect, status: 'done' }] }) });
    api.get
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'pending')] })
      .mockResolvedValueOnce({ conversation: conv, messages: [user(1), bot(2, 'done', { actions: [effect] })] });
    const detach = attachView();
    await openConversation('C1');
    await tick();
    await flush();
    expect(api.report).toHaveBeenCalledWith({ conversationId: 'C1', messageId: 'A2', actionIndex: 0, status: 'done' });
    expect((assistant.messages[1].actions?.[0] as unknown as { status: string }).status).toBe('done');
    detach();
  });

  it('stops polling when the last view goes away or the conversation changes', async () => {
    api.get.mockResolvedValue({ conversation: conv, messages: [user(1), bot(2, 'pending')] });
    const detach = attachView();
    await openConversation('C1');
    detach();
    await tick(5 * POLL_MS);
    expect(api.get).toHaveBeenCalledTimes(1);

    const again = attachView();
    await vi.advanceTimersByTimeAsync(0);
    const calls = api.get.mock.calls.length;
    newConversation();
    await tick(5 * POLL_MS);
    expect(api.get).toHaveBeenCalledTimes(calls);
    again();
  });

  it('drops an answer that comes back after the conversation changed', async () => {
    let release: (v: unknown) => void = () => {};
    api.get.mockReturnValueOnce(new Promise((r) => (release = r)));
    assistant.currentId = 'C1';
    assistant.messages = [user(1), bot(2, 'pending')];
    assistant.loaded = true;
    const detach = attachView();
    await tick();
    newConversation();
    release({ conversation: conv, messages: [user(1), bot(2, 'done')] });
    await vi.advanceTimersByTimeAsync(0);
    expect(assistant.messages).toEqual([]);
    detach();
  });

  it('backs off on errors and gives up with a message', async () => {
    api.get.mockRejectedValue(new Error('down'));
    assistant.currentId = 'C1';
    assistant.messages = [user(1), bot(2, 'pending')];
    assistant.loaded = true;
    const detach = attachView();
    await flush();
    api.get.mockClear();
    await tick(POLL_FAST_MS);
    expect(api.get).toHaveBeenCalledTimes(1);
    await tick(2 * POLL_FAST_MS);
    expect(api.get).toHaveBeenCalledTimes(2);
    await tick(10 * 60 * 1000);
    expect(assistant.error).toContain('down');
    const n = api.get.mock.calls.length;
    await tick(60 * 1000);
    expect(api.get).toHaveBeenCalledTimes(n);
    detach();
  });

  it('keeps the draft and shows the error when a send is refused', async () => {
    assistant.currentId = 'C1';
    api.send.mockRejectedValue(new Error('no model'));
    expect(await send('hello')).toBe(false);
    expect(assistant.error).toBe('no model');
    expect(assistant.sending).toBe(false);
  });

  it('refuses a send while an answer is pending and a message that is too long', async () => {
    assistant.currentId = 'C1';
    assistant.messages = [user(1), bot(2, 'pending')];
    expect(await send('again')).toBe(false);
    expect(api.send).not.toHaveBeenCalled();
    assistant.messages = [];
    expect(await send('x'.repeat(5000))).toBe(false);
    expect(assistant.error).toContain('too long');
  });

  it('retries by sending the text of the user message again', async () => {
    assistant.currentId = 'C1';
    assistant.messages = [user(1, 'start it'), bot(2, 'error', { error: 'boom' })];
    api.send.mockResolvedValue({ userMessage: user(3, 'start it'), assistantMessage: bot(4, 'pending') });
    expect(await retry('A2')).toBe(true);
    expect(api.send).toHaveBeenCalledWith('C1', 'start it', expect.anything(), expect.anything());
    expect(assistant.messages.map((m) => m.id)).toEqual(['U1', 'A2', 'U3', 'A4']);
    resetAssistant();
  });

  it('deleting the current conversation starts a new one', async () => {
    assistant.conversations = [conv];
    assistant.currentId = 'C1';
    assistant.messages = [user(1)];
    api.remove.mockResolvedValue({});
    await deleteConversation('C1');
    expect(assistant.conversations).toEqual([]);
    expect(assistant.currentId).toBe('');
    expect(assistant.messages).toEqual([]);
  });

  it('looks for the answer faster at first, then at the normal cadence, backing off on failures', () => {
    expect(pollDelay(0, 0)).toBe(POLL_FAST_MS);
    expect(pollDelay(POLL_FAST_COUNT - 1, 0)).toBe(POLL_FAST_MS);
    expect(pollDelay(POLL_FAST_COUNT, 0)).toBe(POLL_MS);
    expect(pollDelay(0, 2)).toBe(POLL_FAST_MS * 4);
    expect(pollDelay(100, 10)).toBe(15000);
    expect(POLL_FAST_MS).toBeLessThan(POLL_MS);
  });
});
