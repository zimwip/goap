// State of the contextual helper (ADR 0086): ephemeral, in memory only. Nothing is persisted (no storage, no
// preferences), nothing runs in the engine; closing the helper discards the discussion and the proposals.
import { models, type SuggestMessage, type SuggestProposal } from '../api';
import { activeTab } from '../shell/tabs.svelte';
import { collectContext } from './context';
import { fieldOf, hasFields } from '../assist/registry.svelte';
import { helperEnabled } from './enabled';

/** The discussion is resent each time: the server accepts at most 12 turns, the helper keeps the last ones. */
export const MAX_MESSAGES = 10;

export interface Proposal {
  fieldId: string;
  /** the proposed value */
  value: unknown;
  rationale: string;
}

export interface HelperState {
  open: boolean;
  loading: boolean;
  error: string;
  /** the tab the helper was opened on */
  tab: string;
  messages: SuggestMessage[];
  proposals: Proposal[];
  /** the proposal shown */
  index: number;
  /** the last answer's message, shown with the proposals */
  note: string;
}

export const helper: HelperState = $state({ open: false, loading: false, error: '', tab: '', messages: [], proposals: [], index: 0, note: '' });

let inflight: AbortController | undefined;
let run = 0;

/** Can the helper be opened now: the alias is available and the active tab shows a field it may fill. */
export function canOpenHelper(): boolean {
  return helperEnabled() && hasFields();
}

function reset(): void {
  inflight?.abort();
  inflight = undefined;
  run++;
  helper.open = false;
  helper.loading = false;
  helper.error = '';
  helper.tab = '';
  helper.messages = [];
  helper.proposals = [];
  helper.index = 0;
  helper.note = '';
}

/** The proposal shown, when its field is still on the page. */
export function currentProposal(): Proposal | undefined {
  return helper.proposals[helper.index];
}

function parse(p: SuggestProposal): Proposal | undefined {
  try {
    return { fieldId: p.fieldId, value: JSON.parse(p.value), rationale: p.rationale ?? '' };
  } catch {
    return undefined;
  }
}

/** Asks the server with the current context and the discussion; replaces the proposals with the answer. */
async function ask(instruction = ''): Promise<void> {
  inflight?.abort();
  const ctl = (inflight = new AbortController());
  const mine = ++run;
  helper.loading = true;
  helper.error = '';
  try {
    const context = collectContext(activeTab());
    const res = await models.suggest({ context, messages: helper.messages.slice(-MAX_MESSAGES), instruction: instruction || undefined }, ctl.signal);
    if (mine !== run) return;
    const known = new Set(context.fields.filter((f) => !f.readOnly).map((f) => f.id));
    const proposals = (res.proposals ?? []).flatMap((p) => {
      const q = parse(p);
      return q && known.has(q.fieldId) ? [q] : [];
    });
    helper.proposals = proposals;
    helper.index = 0;
    helper.note = res.message ?? '';
    const said = helper.note || (proposals.length ? `Proposed: ${proposals.map((p) => p.fieldId).join(', ')}` : 'No proposal.');
    helper.messages = [...helper.messages, { role: 'assistant' as const, text: said }].slice(-MAX_MESSAGES);
  } catch (e) {
    if (mine !== run) return;
    helper.error = e instanceof Error ? e.message : String(e);
  } finally {
    if (mine === run) helper.loading = false;
  }
}

/** Opens the helper on the active tab and asks for a first round of proposals. */
export async function openHelper(): Promise<void> {
  if (helper.open || !canOpenHelper()) return;
  reset();
  helper.open = true;
  helper.tab = activeTab()?.id ?? '';
  await ask();
}

/** A refinement from the user: resends the context and the discussion, and replaces the proposals. */
export async function comment(text: string): Promise<void> {
  const t = text.trim();
  if (!helper.open || !t) return;
  helper.messages = [...helper.messages, { role: 'user' as const, text: t }].slice(-MAX_MESSAGES);
  await ask();
}

/** Puts the shown proposal in its field, through the setter of the form, then moves to the next or closes. */
export function accept(): void {
  const p = currentProposal();
  if (!p) return;
  const f = fieldOf(p.fieldId, helper.tab);
  if (!f) {
    helper.error = `The field “${p.fieldId}” is no longer on the page.`;
    return;
  }
  try {
    f.spec.set(p.value);
  } catch (e) {
    helper.error = e instanceof Error ? e.message : String(e);
    return;
  }
  helper.error = '';
  if (helper.index + 1 < helper.proposals.length) helper.index++;
  else closeHelper();
}

/** Leaves the helper (Reject): the discussion and the proposals are discarded. */
export function closeHelper(): void {
  reset();
}
