import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { assistantApi, type CreateChangeAction } from '../api';

// the assistant client: one POST to the service, with the proto3 JSON body

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  const m = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
  });
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('assistantApi', () => {
  it('sends a message with the context of the page and gets the pending answer', async () => {
    fetchMock.mockReturnValueOnce(
      Promise.resolve(
        new Response(
          JSON.stringify({
            userMessage: { id: 'M1', seq: 1, role: 'user', status: 'done', text: 'hi', context: 'tab change, project PROJ-A' },
            assistantMessage: { id: 'M2', seq: 2, role: 'assistant', status: 'pending' },
          }),
          { status: 200 },
        ),
      ),
    );
    const r = await assistantApi.send('CONV-1', 'hi', { tab: { kind: 'change', params: { id: 'CHG-1' } }, subject: 'CHG-1', project: 'PROJ-A' });
    expect(r.assistantMessage.status).toBe('pending');
    expect(r.userMessage.context).toContain('PROJ-A');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/goap.assistant.v1.AssistantService/Send');
    expect(JSON.parse(init.body as string)).toEqual({
      conversationId: 'CONV-1',
      text: 'hi',
      context: { tab: { kind: 'change', params: { id: 'CHG-1' } }, subject: 'CHG-1', project: 'PROJ-A' },
    });
  });

  it('sends an empty context by default, and types the actions of a created change', async () => {
    fetchMock.mockReturnValueOnce(Promise.resolve(new Response(JSON.stringify({}), { status: 200 })));
    await assistantApi.send('CONV-1', 'hello');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string).context).toEqual({});
    const a: CreateChangeAction = { type: 'create_change', args: { title: 't', intent: 'i' }, result: { changeId: 'CHG-9' } };
    expect(a.result.changeId).toBe('CHG-9');
  });
});
