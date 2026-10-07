import { rpc } from './transport';
import type { AssistantContext, AssistantSendResponse } from './types/assistant';

const ASSISTANT = 'goap.assistant.v1.AssistantService';

/**
 * The conversational assistant (ADR 0087). `send` appends the user message to a conversation of the caller and a pending
 * assistant message, and returns both at once: the answer is written into that message in the background, so the caller
 * polls `conversationsApi.get` until it is no longer `pending` and then runs its `actions`. It is refused with
 * FAILED_PRECONDITION when the caller has no `assistant` model alias, or the previous message is still being answered.
 */
export const assistantApi = {
  send: (conversationId: string, text: string, context: AssistantContext = {}) =>
    rpc<{ conversationId: string; text: string; context: AssistantContext }, AssistantSendResponse>(ASSISTANT, 'Send', {
      conversationId,
      text,
      context,
    }),
};
