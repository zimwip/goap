// Conversation service types (proto3 JSON, ADR 0085).

import type { Struct } from './common';

export type MessageRole = 'user' | 'assistant';
/** `cancelled`: the person stopped the answer (it keeps the actions recorded before). */
export type MessageStatus = 'pending' | 'done' | 'error' | 'cancelled';

/** A UI action the assistant asked the web to run; `type` and `args` are the web's own vocabulary. */
export interface ConversationAction {
  type: string;
  args?: Struct;
  result?: Struct;
}

export interface Conversation {
  id: string;
  subject: string;
  title: string;
  /** RFC 3339 */
  createdAt?: string;
  updatedAt?: string;
}

export interface ConversationMessage {
  id: string;
  conversationId: string;
  /** position in the conversation, from 1 */
  seq: number;
  role: MessageRole;
  text?: string;
  actions?: ConversationAction[];
  /** the engine run that produced an assistant message */
  processId?: string;
  status: MessageStatus;
  error?: string;
  /** a user message: a short description of what the user was looking at (never the page itself); an assistant
   *  message: where the assistant worked (outside any project, a project, a change) */
  context?: string;
  createdAt?: string;
}
