// Assistant service types (proto3 JSON, ADR 0087).

import type { Struct } from './common';
import type { ConversationAction, ConversationMessage } from './conversations';

/**
 * What the person was looking at when they wrote the message, in three layers the server reads most specific first
 * (ADR 0092): the focus, the screen and the application. Used for that turn only, never stored. Empty fields are left
 * out. The server caps and sanitises: about 8 KiB in all, at most 40 `screen.entities` (the one of `focus.element`
 * first kept), 10 `focus.errors`, 6 props per entity, texts cut (labels 200 bytes, summary 600, pending/last action 300,
 * errors 200), control characters blanked; the `focus.selection` over 2000 bytes is refused.
 */
export interface AssistantContext {
  app?: {
    /** the active project key */
    project?: string;
    /** a change tab is `{ kind: 'change', params: { id } }`: the assistant takes the change it is about from there */
    tab?: { kind: string; params?: Record<string, string> };
  };
  screen?: {
    kind?: string;
    title?: string;
    /** a short text of what the view shows */
    summary?: string;
    entities?: AssistantEntity[];
  };
  focus?: {
    /** the element the person acts on */
    element?: { type: string; id: string; label?: string };
    /** the text selected on the page (at most 2000 bytes) */
    selection?: string;
    dialogKind?: string;
    dialogTitle?: string;
    /** what the person is in the middle of, e.g. "reviewing impact X of change Y" */
    pendingAction?: string;
    /** the errors shown on the screen (at most 10) */
    errors?: string[];
    lastAction?: string;
  };
}

export interface AssistantEntity {
  type: string;
  id: string;
  label?: string;
  state?: string;
  /** a few small properties (at most 6) */
  props?: Record<string, string>;
}

/**
 * A param of a screen tool: the subset of JSON Schema the server accepts. `enum` is a type of its own (with `enum`
 * listing its values, 1 to 30); an `array` takes `items`, a scalar or enum param (never nested).
 */
export interface UiParam {
  type: 'string' | 'number' | 'boolean' | 'enum' | 'array';
  description?: string;
  enum?: string[];
  items?: UiParam;
}

/** The arguments of a screen tool: at most 12 properties named `[A-Za-z][A-Za-z0-9_]{0,39}`, `required` among them. */
export interface UiArgs {
  properties?: Record<string, UiParam>;
  required?: string[];
}

/**
 * A tool the current screen offers this turn (ADR 0092), sent with each message (at most 30). The server never runs it:
 * an `effect` (no data changes: navigate, filter, select, open, focus) comes back as a `requested` action to run at
 * once, a `write` as a `proposed` action to confirm first.
 */
export interface UiTool {
  /** `[a-z][a-z0-9_.-]{0,48}`, unique in the request; the model calls it `ui.<name>` */
  name: string;
  /** at most 300 bytes */
  description: string;
  /** one line (200 bytes): what is required to feed it */
  guidance?: string;
  level: 'effect' | 'write';
  args?: UiArgs;
  /** the element id it acts on */
  target?: string;
}

export interface AssistantSendResponse {
  userMessage: ConversationMessage;
  /** status `pending`: poll the conversation until it is `done` or `error` */
  assistantMessage: ConversationMessage;
}

/** The UI actions the assistant can ask the web to run (the `type` of a `ConversationAction`). */
export type AssistantActionType = 'select_project' | 'create_change' | 'open_change' | 'start_agent' | 'ui_tool';

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

/**
 * `requested`: an effect to run at once; `proposed`: a write waiting for the person; `accepted`: the person accepted,
 * the web runs it through the edit path of the screen and reports; `rejected`; `done` / `failed`: the reported outcome.
 */
export type UiToolStatus = 'requested' | 'proposed' | 'accepted' | 'rejected' | 'done' | 'failed';

/**
 * An action of a screen tool (ADR 0092). `tool` is the descriptor name (without the `ui.` prefix), `args` are validated
 * against its schema. For a `write`: `confirmAction` accept / reject (accept only marks it `accepted`; run the tool, then
 * `reportAction`); `project` is the project it was proposed in (accept is refused as stale in another one, reject is
 * always possible). An `effect` is run when seen `requested`; reporting its outcome is optional.
 */
export interface UiToolAction extends ConversationAction {
  type: 'ui_tool';
  status: UiToolStatus;
  level: 'effect' | 'write';
  tool: string;
  args: Struct;
  label: string;
  rationale?: string;
  target?: string;
  /** write only: the project the proposal was made in */
  project?: string;
  error?: string;
  decided?: { by: string; at: string; decision: 'accept' | 'reject' };
  reported?: { by: string; at: string };
}

export interface ReportActionRequest {
  conversationId: string;
  messageId: string;
  actionIndex: number;
  status: 'done' | 'failed';
  /** what went wrong, for `failed` */
  error?: string;
}

/** The assistant message with its action updated. */
export interface ReportActionResponse {
  message: ConversationMessage;
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
