// The proposal card of the assistant (ADR 0090, 0092): what a `start_agent` or a write `ui_tool` action shows while it
// waits for the person and afterwards, and the status of the run an agent started. Pure: the card component and the
// tests read the same view.
import type { ConversationAction, Process, StartAgentAction, UiToolAction } from '../api';

export type CardKind = 'start_agent' | 'ui_tool';

/**
 * proposed: waits for the person · starting: the server claimed it · started: the agent runs · applying: accepted, the
 * screen is applying it · unapplied: accepted but its outcome was never reported (a reload) · done · failed · rejected.
 */
export type CardStatus = 'proposed' | 'starting' | 'started' | 'applying' | 'unapplied' | 'done' | 'failed' | 'rejected';

export const STATUS_LABEL: Record<CardStatus, string> = {
  proposed: 'Waiting for your decision',
  starting: 'Starting…',
  started: 'Started',
  applying: 'Applying…',
  unapplied: 'Accepted, not applied',
  done: 'Done',
  failed: 'Failed',
  rejected: 'Rejected',
};

export interface CardView {
  kind: CardKind;
  status: CardStatus;
  statusLabel: string;
  title: string;
  details: { label: string; value: string }[];
  rationale: string;
  error: string;
  acceptLabel: string;
  /** Accept and Reject are offered */
  canDecide: boolean;
  /** an accepted write whose outcome is unknown, and whose tool is on the screen now */
  canRetry: boolean;
  /** why it cannot be retried (the tool is no longer offered) */
  retryBlocked: string;
  /** the element a write points at (`type:id`, from the descriptor or the arguments) */
  target: string;
  processId: string;
  changeId: string;
}

const clip = (s: string, n = 160): string => (s.length <= n ? s : s.slice(0, n - 1) + '…');

/** Does this action get a card (a proposal)? Effects are chips. */
export function isCard(a: ConversationAction): boolean {
  return a.type === 'start_agent' || (a.type === 'ui_tool' && (a as UiToolAction).level === 'write');
}

/** The arguments of a screen tool as label / value rows, texts clipped. */
export function argsSummary(args: unknown): { label: string; value: string }[] {
  return Object.entries((args ?? {}) as Record<string, unknown>)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([k, v]) => ({ label: k, value: clip(typeof v === 'string' ? v : JSON.stringify(v)) }));
}

function startAgentStatus(a: StartAgentAction): CardStatus {
  switch (a.status) {
    case 'started':
    case 'starting':
    case 'rejected':
    case 'failed':
      return a.status;
    default:
      return 'proposed';
  }
}

function uiToolStatus(a: UiToolAction, busy: boolean): CardStatus {
  switch (a.status) {
    case 'accepted':
      return busy ? 'applying' : 'unapplied';
    case 'done':
    case 'failed':
    case 'rejected':
      return a.status;
    default:
      return 'proposed';
  }
}

/**
 * The card of an action, or undefined for one that is no proposal. `busy`: the web is deciding or applying it now;
 * `registered`: the screen offers the tool of a `ui_tool` now (for the retry of an accepted one).
 */
export function cardView(a: ConversationAction, opts: { busy?: boolean; registered?: boolean; target?: string } = {}): CardView | undefined {
  const busy = !!opts.busy;
  if (a.type === 'start_agent') {
    const s = a as StartAgentAction;
    const args = s.args ?? ({} as StartAgentAction['args']);
    const status = startAgentStatus(s);
    const details: CardView['details'] = [
      { label: 'Methodology', value: args.methodology ?? '' },
      { label: 'Agent', value: args.agent ?? '' },
      { label: 'Goal', value: args.goal ?? '' },
      { label: 'Intent', value: clip(args.intent ?? '') },
      args.changeId
        ? { label: 'Change', value: args.changeId }
        : { label: 'New change', value: args.newChange ? `${args.newChange.title}${args.newChange.intent ? ` — ${clip(args.newChange.intent)}` : ''}` : '' },
      { label: 'Project', value: args.project ?? '' },
    ].filter((d) => d.value);
    return {
      kind: 'start_agent',
      status,
      statusLabel: STATUS_LABEL[status],
      title: s.label || `Run the agent ${args.agent ?? ''}`.trim(),
      details,
      rationale: s.rationale ?? '',
      error: s.error ?? '',
      acceptLabel: 'Launch',
      canDecide: status === 'proposed' && !busy,
      canRetry: false,
      retryBlocked: '',
      target: '',
      processId: s.result?.processId ?? '',
      changeId: s.result?.changeId ?? args.changeId ?? '',
    };
  }
  if (a.type === 'ui_tool' && (a as UiToolAction).level === 'write') {
    const u = a as UiToolAction;
    const status = uiToolStatus(u, busy);
    return {
      kind: 'ui_tool',
      status,
      statusLabel: STATUS_LABEL[status],
      title: u.label || u.tool,
      details: argsSummary(u.args),
      rationale: u.rationale ?? '',
      error: u.error ?? '',
      acceptLabel: 'Apply',
      canDecide: status === 'proposed' && !busy,
      canRetry: status === 'unapplied' && !!opts.registered,
      retryBlocked: status === 'unapplied' && !opts.registered ? 'The screen it was proposed for is not open: open it and ask again.' : '',
      target: u.target || opts.target || '',
      processId: '',
      changeId: '',
    };
  }
  return undefined;
}

export interface RunStatus {
  label: string;
  tone: 'ok' | 'err' | 'warn' | 'muted';
}

/** Where a started agent's run stands, from its process (undefined: not read yet). */
export function runStatus(p: Pick<Process, 'status' | 'pending'> | undefined): RunStatus {
  if (!p) return { label: 'Reading the run…', tone: 'muted' };
  switch (p.status) {
    case 'running':
      return { label: 'Running', tone: 'ok' };
    case 'waiting': {
      const k = p.pending?.kind;
      const what = k === 'approval' ? 'approval' : k === 'input' ? 'input' : k === 'condition' ? 'conditions' : k === 'agent' ? 'a sub-agent' : '';
      return { label: what ? `Waiting for ${what}` : 'Waiting', tone: 'warn' };
    }
    case 'completed':
      return { label: 'Completed', tone: 'ok' };
    case 'failed':
      return { label: 'Failed', tone: 'err' };
    case 'stuck':
      return { label: 'Stuck: needs a person to unblock it', tone: 'err' };
    case 'clarifying':
      return { label: 'Clarifying the intent', tone: 'warn' };
    case 'superseded':
      return { label: 'Superseded', tone: 'muted' };
    default:
      return { label: String(p.status ?? 'Unknown'), tone: 'muted' };
  }
}
