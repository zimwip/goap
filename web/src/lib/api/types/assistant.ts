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
export type AssistantActionType = 'select_project' | 'create_change' | 'open_change';

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
