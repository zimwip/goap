import { rpc } from './transport';
import type {
  AssistantContext,
  AssistantSendResponse,
  ConfirmActionRequest,
  ConfirmActionResponse,
  ReportActionRequest,
  ReportActionResponse,
  UiTool,
} from './types/assistant';

const ASSISTANT = 'goap.assistant.v1.AssistantService';

/**
 * The conversational assistant (ADR 0087). `send` appends the user message to a conversation of the caller and a pending
 * assistant message, and returns both at once: the answer is written into that message in the background, so the caller
 * polls `conversationsApi.get` until it is no longer `pending` and then runs its `actions`. It is refused with
 * FAILED_PRECONDITION when the caller has no `assistant` model alias, or the previous message is still being answered.
 */
export const assistantApi = {
  send: (conversationId: string, text: string, context: AssistantContext = {}, uiTools: UiTool[] = []) =>
    rpc<{ conversationId: string; text: string; context: AssistantContext; uiTools: UiTool[] }, AssistantSendResponse>(ASSISTANT, 'Send', {
      conversationId,
      text,
      context,
      uiTools,
    }),
  /**
   * Decides a proposal: to run an agent (ADR 0090) or to apply a screen tool (`ui_tool`, ADR 0092: accept only marks it
   * `accepted`, the web then runs it and calls `reportAction`). ABORTED: already decided; FAILED_PRECONDITION: the proposal is stale
   * (other project, change closed, agent no longer runnable) and stays proposed. A start that fails is not an error: the
   * returned action has status `failed`.
   */
  confirmAction: (req: ConfirmActionRequest) => rpc<ConfirmActionRequest, ConfirmActionResponse>(ASSISTANT, 'ConfirmAction', req),
  /**
   * Records the outcome of a screen tool the web ran (ADR 0092): from `accepted` (write) or `requested` (effect) to `done`
   * or `failed`. FAILED_PRECONDITION: the action is in another state; ABORTED: an outcome was already reported.
   */
  reportAction: (req: ReportActionRequest) => rpc<ReportActionRequest, ReportActionResponse>(ASSISTANT, 'ReportAction', req),
};
