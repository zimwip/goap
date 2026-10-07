// Assistant service types (proto3 JSON, ADR 0087).

import type { ConversationAction, ConversationMessage } from './conversations';

/** What the person was looking at when they wrote the message: used for that turn only, never stored. */
export interface AssistantContext {
  tab?: { kind: string; params?: Record<string, string> };
  /** the node or change the tab is about (opaque) */
  subject?: string;
  /** the text selected on the page (at most 2000 bytes) */
  selection?: string;
  /** the active project key */
  project?: string;
}

export interface AssistantSendResponse {
  userMessage: ConversationMessage;
  /** status `pending`: poll the conversation until it is `done` or `error` */
  assistantMessage: ConversationMessage;
}

/** The UI actions the assistant can ask the web to run (the `type` of a `ConversationAction`). */
export type AssistantActionType = 'select_project' | 'create_change' | 'open_change' | 'start_agent';

export interface SelectProjectAction extends ConversationAction {
  type: 'select_project';
  args: { project: string };
}

/** A change the assistant created for the person; `result.changeId` is the one to open. */
export interface CreateChangeAction extends ConversationAction {
  type: 'create_change';
  args: { title: string; intent: string; methodology?: string; project?: string };
  result: { changeId: string; title?: string; project?: string; methodology?: string };
}

export interface OpenChangeAction extends ConversationAction {
  type: 'open_change';
  args: { changeId: string };
  result?: { changeId: string; title?: string; project?: string; status?: string };
}

/** `proposed` waits for the person; `starting` is a transient claim; the others are final (ADR 0090). */
export type StartAgentStatus = 'proposed' | 'starting' | 'started' | 'rejected' | 'failed';

/**
 * A proposal to run an agent (ADR 0090). Nothing is started until `confirmAction(..., 'accept')`. `args.changeId` is the
 * change the agent works on, else `args.newChange` is created first. After the decision `status` is `started` (with
 * `result.processId`, read the run from the process), `rejected` or `failed` (with `error`).
 */
export interface StartAgentAction extends ConversationAction {
  type: 'start_agent';
  status: StartAgentStatus;
  label: string;
  rationale?: string;
  args: {
    methodology: string;
    agent: string;
    goal?: string;
    intent?: string;
    /** the project the proposal was made in */
    project: string;
    changeId?: string;
    newChange?: { title: string; intent: string };
  };
  result?: { processId?: string; changeId?: string };
  error?: string;
  decided?: { by: string; at: string; decision: 'accept' | 'reject' };
}

export interface ConfirmActionRequest {
  conversationId: string;
  messageId: string;
  actionIndex: number;
  decision: 'accept' | 'reject';
  /** the active project of the web */
  project?: string;
}

/** The assistant message with its action updated. */
export interface ConfirmActionResponse {
  message: ConversationMessage;
}
