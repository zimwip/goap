// The decisions on the proposals of the assistant (ADR 0090, 0092): Accept / Reject of a `start_agent` or a write
// screen tool, the application of an accepted screen tool through the registry, and what the card says meanwhile.
// Memory only. A write never runs before the server recorded the acceptance; its outcome is reported after, so a
// reload shows `accepted` without an outcome as "not applied", which can be retried while the screen offers the tool.
import { assistantApi, errorMessage, type ConversationMessage, type UiToolAction } from '../api';
import { runUiTool } from '../assistant/actions';
import { project } from './project.svelte';
import { applyMessage, assistant, openConversation, reportOutcome } from './assistant.svelte';

export const proposals = $state({
  /** per action key: what the web is doing with it now */
  busy: {} as Record<string, 'deciding' | 'applying'>,
  /** per action key: what went wrong, for the card */
  notes: {} as Record<string, string>,
});

export const proposalKey = (m: Pick<ConversationMessage, 'id'>, index: number): string => `${m.id}:${index}`;

const codeOf = (e: unknown): string => (e as { code?: string } | undefined)?.code ?? '';
export const STALE_NOTE = 'This proposal is no longer valid (the project or the situation changed). You can reject it.';

async function reload(): Promise<void> {
  if (assistant.currentId) await openConversation(assistant.currentId, true);
}

/** What a failed call says to the card; an already decided proposal is read again. */
async function explain(key: string, e: unknown): Promise<void> {
  const c = codeOf(e);
  if (c === 'aborted') {
    proposals.notes[key] = 'This proposal was already decided; the conversation is read again.';
    await reload();
  } else if (c === 'failed_precondition') {
    proposals.notes[key] = STALE_NOTE;
  } else {
    proposals.notes[key] = errorMessage(e);
  }
}

/** Runs an accepted screen tool and reports the outcome. The tool must be registered now. */
async function applyAccepted(m: ConversationMessage, index: number, key: string): Promise<void> {
  const a = m.actions?.[index] as UiToolAction | undefined;
  if (!a) return;
  proposals.busy[key] = 'applying';
  try {
    const r = await runUiTool(a);
    try {
      await reportOutcome(m, index, r.ok ? 'done' : 'failed', r.ok ? undefined : r.error);
    } catch (e) {
      // the tool ran: a report refused (already reported, other state) is read again, any other is shown
      if (codeOf(e) === 'aborted' || codeOf(e) === 'failed_precondition') await reload();
      else proposals.notes[key] = `The outcome could not be recorded: ${errorMessage(e)}`;
    }
  } finally {
    delete proposals.busy[key];
  }
}

/** Accept or Reject of a proposal (`start_agent` or a write `ui_tool`). */
export async function decide(m: ConversationMessage, index: number, decision: 'accept' | 'reject'): Promise<void> {
  const key = proposalKey(m, index);
  if (proposals.busy[key]) return;
  proposals.busy[key] = 'deciding';
  delete proposals.notes[key];
  let next: ConversationMessage | undefined;
  try {
    const r = await assistantApi.confirmAction({ conversationId: m.conversationId, messageId: m.id, actionIndex: index, decision, project: project.current });
    next = r.message;
    applyMessage(r.message);
  } catch (e) {
    delete proposals.busy[key];
    await explain(key, e);
    return;
  }
  const a = next?.actions?.[index];
  if (decision === 'accept' && next && a?.type === 'ui_tool' && (a as UiToolAction).status === 'accepted') {
    await applyAccepted(next, index, key);
    return;
  }
  delete proposals.busy[key];
}

/** Applies again an accepted screen tool whose outcome was never reported (the tool must be on the screen now). */
export async function retryApplied(m: ConversationMessage, index: number, registered: boolean): Promise<void> {
  const key = proposalKey(m, index);
  if (proposals.busy[key]) return;
  const a = m.actions?.[index] as UiToolAction | undefined;
  if (a?.type !== 'ui_tool' || a.status !== 'accepted') return;
  if (!registered) {
    proposals.notes[key] = 'The screen it was proposed for is not open: open it and ask again.';
    return;
  }
  delete proposals.notes[key];
  await applyAccepted(m, index, key);
}

export function resetProposals(): void {
  for (const k of Object.keys(proposals.busy)) delete proposals.busy[k];
  for (const k of Object.keys(proposals.notes)) delete proposals.notes[k];
}
