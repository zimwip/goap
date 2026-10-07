import { rpc } from './transport';
import type { Empty } from './types/common';
import type { Conversation, ConversationMessage } from './types/conversations';

const CONVERSATIONS = 'goap.conversations.v1.ConversationService';

/**
 * The conversations of the caller with the assistant, kept outside the graph (ADR 0085). A caller only ever reads and
 * writes their own; the assistant's messages are written by the platform, so the web only appends user messages and
 * polls `get` while an assistant message is pending (there is no event).
 */
export const conversationsApi = {
  create: (title = '') => rpc<{ title: string }, { conversation: Conversation }>(CONVERSATIONS, 'CreateConversation', { title }),
  /** Most recently updated first; pass `nextPageToken` of the previous page to continue. */
  list: (pageSize = 0, pageToken = '', signal?: AbortSignal) =>
    rpc<{ pageSize: number; pageToken: string }, { conversations?: Conversation[]; nextPageToken?: string }>(
      CONVERSATIONS,
      'ListConversations',
      { pageSize, pageToken },
      signal,
    ),
  get: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { conversation: Conversation; messages?: ConversationMessage[] }>(CONVERSATIONS, 'GetConversation', { id }, signal),
  rename: (id: string, title: string) =>
    rpc<{ id: string; title: string }, { conversation: Conversation }>(CONVERSATIONS, 'RenameConversation', { id, title }),
  remove: (id: string) => rpc<{ id: string }, Empty>(CONVERSATIONS, 'DeleteConversation', { id }),
  /** Appends a user message to a conversation of the caller. */
  append: (conversationId: string, text: string) =>
    rpc<{ conversationId: string; text: string }, { message: ConversationMessage }>(CONVERSATIONS, 'AppendMessage', { conversationId, text }),
};
