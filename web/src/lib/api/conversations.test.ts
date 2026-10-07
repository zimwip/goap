import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { conversationsApi } from '../api';

// the conversation client: each call is one POST to the service, with the proto3 JSON body

let fetchMock: ReturnType<typeof vi.fn>;

function reply(body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
}

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

const SVC = '/goap.conversations.v1.ConversationService/';

function call(i = 0) {
  const [url, init] = fetchMock.mock.calls[i];
  return { url: url as string, body: JSON.parse(init.body as string) };
}

describe('conversationsApi', () => {
  it('creates, lists and pages', async () => {
    fetchMock.mockReturnValueOnce(reply({ conversation: { id: 'CONV-1', title: 'New conversation' } }));
    expect((await conversationsApi.create()).conversation.id).toBe('CONV-1');
    expect(call()).toEqual({ url: SVC + 'CreateConversation', body: { title: '' } });

    fetchMock.mockReturnValueOnce(reply({ conversations: [{ id: 'CONV-1' }], nextPageToken: 'tok' }));
    const page = await conversationsApi.list(20, 'prev');
    expect(page.nextPageToken).toBe('tok');
    expect(call(1)).toEqual({ url: SVC + 'ListConversations', body: { pageSize: 20, pageToken: 'prev' } });
  });

  it('reads a conversation with its messages and appends a user message', async () => {
    fetchMock.mockReturnValueOnce(
      reply({
        conversation: { id: 'CONV-1' },
        messages: [{ id: 'M1', seq: 1, role: 'assistant', status: 'done', actions: [{ type: 'open_change', args: { id: 'CHG-1' } }] }],
      }),
    );
    const got = await conversationsApi.get('CONV-1');
    expect(got.messages?.[0].actions?.[0].type).toBe('open_change');
    expect(call()).toEqual({ url: SVC + 'GetConversation', body: { id: 'CONV-1' } });

    fetchMock.mockReturnValueOnce(reply({ message: { id: 'M2', seq: 2, role: 'user', status: 'done' } }));
    await conversationsApi.append('CONV-1', 'hello');
    expect(call(1)).toEqual({ url: SVC + 'AppendMessage', body: { conversationId: 'CONV-1', text: 'hello' } });
  });

  it('renames and removes', async () => {
    fetchMock.mockImplementation(() => reply({}));
    await conversationsApi.rename('CONV-1', 'Release');
    await conversationsApi.remove('CONV-1');
    expect(call()).toEqual({ url: SVC + 'RenameConversation', body: { id: 'CONV-1', title: 'Release' } });
    expect(call(1)).toEqual({ url: SVC + 'DeleteConversation', body: { id: 'CONV-1' } });
  });
});
