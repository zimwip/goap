// The conversational assistant (ADR 0087): conversations of the caller from the conversation service (ADR 0085), the
// messages of the current one, and the polling of an answer being written. Memory only: the current conversation is not
// kept in the browser (ADR 0052), a new page opens the most recent one.
import { assistantApi, conversationsApi, errorMessage, type Conversation, type ConversationMessage } from '../api';
import { activeTab } from '../shell/tabs.svelte';
import { assistantContext, clipBytes } from '../assistant/context';
import { runActions } from '../assistant/actions';
import { describeTools } from '../assist/registry.svelte';
import { forgetSelection } from '../assist/capture';
import { untrack } from 'svelte';
import { project } from './project.svelte';

/** The server accepts a message of at most 4 KiB (assistantsvc `MaxTextBytes`). */
export const MAX_TEXT_BYTES = 4 << 10;
export const POLL_MS = 1500;
const POLL_MAX_MS = 15000;
/** consecutive failed polls after which polling stops and the error is shown */
const POLL_GIVE_UP = 6;
const TITLE_MAX = 60;

export const assistant = $state({
  conversations: [] as Conversation[],
  /** the current conversation ('' : a new one, created with its first message) */
  currentId: '',
  messages: [] as ConversationMessage[],
  /** the draft of the input box, shared by the tab and the floating panel */
  draft: '',
  loaded: false,
  loading: false,
  sending: false,
  error: '',
  /** the floating panel */
  panelOpen: false,
  /** bumped to ask the input box to take the focus */
  focus: 0,
});

let viewers = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
let fails = 0;
let inflight: AbortController | undefined;
/** one token per load / switch: an answer that comes back for an older one is dropped */
let epoch = 0;
/** the assistant messages seen pending, whose actions run when they turn done (so only ones that turned done here) */
const watching = new Set<string>();

export const lastMessage = (): ConversationMessage | undefined => assistant.messages[assistant.messages.length - 1];

/** Is an answer being written. */
export const isPending = (): boolean => lastMessage()?.role === 'assistant' && lastMessage()?.status === 'pending';

export const currentConversation = (): Conversation | undefined => assistant.conversations.find((c) => c.id === assistant.currentId);

function stopPolling(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  inflight?.abort();
  inflight = undefined;
}

function schedulePoll(): void {
  if (timer || viewers <= 0 || !assistant.currentId || !isPending()) return;
  const delay = Math.min(POLL_MS * 2 ** fails, POLL_MAX_MS);
  timer = setTimeout(() => {
    timer = undefined;
    void poll();
  }, delay);
}

async function poll(): Promise<void> {
  if (viewers <= 0 || !assistant.currentId || !isPending()) return;
  // nothing is fetched while the tab is hidden
  if (typeof document !== 'undefined' && document.hidden) return schedulePoll();
  const mine = epoch;
  const id = assistant.currentId;
  const ctrl = new AbortController();
  inflight = ctrl;
  try {
    const r = await conversationsApi.get(id, ctrl.signal);
    if (mine !== epoch) return;
    fails = 0;
    assistant.error = '';
    apply(r.conversation, r.messages ?? []);
  } catch (e) {
    if (mine !== epoch || ctrl.signal.aborted) return;
    fails++;
    if (fails >= POLL_GIVE_UP) {
      assistant.error = `Lost track of the answer: ${errorMessage(e)}`;
      fails = 0;
      return;
    }
  } finally {
    if (inflight === ctrl) inflight = undefined;
  }
  schedulePoll();
}

/** Takes the messages of the server; runs the actions of the ones that turned done since they were seen pending. */
function apply(c: Conversation | undefined, msgs: ConversationMessage[]): void {
  const sorted = [...msgs].sort((a, b) => a.seq - b.seq);
  const ready: ConversationMessage[] = [];
  for (const m of sorted) {
    if (m.role !== 'assistant') continue;
    if (m.status === 'pending') watching.add(m.id);
    else if (watching.delete(m.id) && m.status === 'done' && m.actions?.length) ready.push(m);
  }
  assistant.messages = sorted;
  if (c?.id) assistant.conversations = assistant.conversations.map((x) => (x.id === c.id ? { ...x, ...c } : x));
  // the actions of different messages run in order, none of them twice: an effect screen tool runs and reports, a
  // proposal waits for its card
  void (async () => {
    for (const m of ready) await runActions(m, (i, status, error) => reportOutcome(m, i, status, error));
  })();
}

/** Puts a message the server returned (an action decided or reported) in the conversation, in its place. */
export function applyMessage(m: ConversationMessage): void {
  if (m.conversationId !== assistant.currentId) return;
  const i = assistant.messages.findIndex((x) => x.id === m.id);
  if (i >= 0) assistant.messages[i] = m;
}

/** Records the outcome of a screen tool the web ran (ADR 0092) and takes the updated message. */
export async function reportOutcome(m: Pick<ConversationMessage, 'conversationId' | 'id'>, index: number, status: 'done' | 'failed', error?: string): Promise<void> {
  const r = await assistantApi.reportAction({ conversationId: m.conversationId, messageId: m.id, actionIndex: index, status, ...(error ? { error } : {}) });
  if (r.message) applyMessage(r.message);
}

/** A view of the conversation (the tab, the floating panel) is shown: polling runs while there is at least one. */
export function attachView(): () => void {
  // attached from an $effect: nothing it reads here (the loading flags, the current id) may become a dependency of
  // that effect, or loading re-runs the effect, which attaches again and loads again: a request storm
  untrack(() => {
    viewers++;
    void ensureLoaded();
    schedulePoll();
  });
  let released = false;
  return () => {
    if (released) return;
    released = true;
    viewers--;
    if (viewers <= 0) {
      viewers = 0;
      stopPolling();
    }
  };
}

async function ensureLoaded(): Promise<void> {
  if (assistant.loading) return;
  if (!assistant.loaded) {
    await loadConversations(true);
  } else if (assistant.currentId) {
    // shown again: what was answered meanwhile
    await openConversation(assistant.currentId, true);
  }
}

/** Reads the list (newest first); the first time, opens the most recent conversation. */
export async function loadConversations(openLatest = false): Promise<void> {
  assistant.loading = true;
  try {
    const r = await conversationsApi.list(50);
    assistant.conversations = r.conversations ?? [];
    assistant.error = '';
    assistant.loaded = true;
    if (openLatest && !assistant.currentId && assistant.conversations.length) await openConversation(assistant.conversations[0].id);
  } catch (e) {
    assistant.error = errorMessage(e);
  } finally {
    assistant.loading = false;
  }
}

/** Makes a conversation the current one and reads its messages. `keep`: re-reading the current one, nothing is reset. */
export async function openConversation(id: string, keep = false): Promise<void> {
  if (!keep) {
    stopPolling();
    epoch++;
    fails = 0;
    assistant.currentId = id;
    assistant.messages = [];
  }
  const mine = epoch;
  assistant.loading = true;
  try {
    const r = await conversationsApi.get(id);
    if (mine !== epoch || assistant.currentId !== id) return;
    assistant.error = '';
    apply(r.conversation, r.messages ?? []);
    schedulePoll();
  } catch (e) {
    if (mine === epoch) assistant.error = errorMessage(e);
  } finally {
    assistant.loading = false;
  }
}

/** A new conversation: it is created with its first message. */
export function newConversation(): void {
  stopPolling();
  epoch++;
  fails = 0;
  assistant.currentId = '';
  assistant.messages = [];
  assistant.error = '';
  assistant.focus++;
}

const titleOf = (text: string): string => {
  const t = text.trim().replace(/\s+/g, ' ');
  return t.length <= TITLE_MAX ? t : t.slice(0, TITLE_MAX - 1) + '…';
};

/**
 * Sends a message with the context of the page. True when the server took it (the caller clears its draft), false with
 * `assistant.error` set otherwise.
 */
export async function send(text: string): Promise<boolean> {
  const t = text.trim();
  if (!t || assistant.sending || isPending()) return false;
  if (new TextEncoder().encode(t).length > MAX_TEXT_BYTES) {
    assistant.error = `The message is too long: at most ${MAX_TEXT_BYTES} bytes.`;
    return false;
  }
  assistant.sending = true;
  assistant.error = '';
  stopPolling();
  const mine = ++epoch;
  try {
    let id = assistant.currentId;
    if (!id) {
      const created = (await conversationsApi.create(clipBytes(titleOf(t), 200))).conversation;
      if (mine !== epoch) return false;
      id = created.id;
      assistant.currentId = id;
      assistant.conversations = [created, ...assistant.conversations.filter((c) => c.id !== id)];
    }
    const tab = activeTab();
    const r = await assistantApi.send(id, t, assistantContext(tab, project.current), describeTools(tab?.id ?? ''));
    forgetSelection();
    if (mine !== epoch) return true;
    watching.add(r.assistantMessage.id);
    assistant.messages = [...assistant.messages.filter((m) => m.id !== r.userMessage.id && m.id !== r.assistantMessage.id), r.userMessage, r.assistantMessage].sort(
      (a, b) => a.seq - b.seq,
    );
    schedulePoll();
    return true;
  } catch (e) {
    if (mine === epoch) assistant.error = errorMessage(e);
    return false;
  } finally {
    assistant.sending = false;
  }
}

/** Sends again the text of the user message before an assistant message that failed. */
export async function retry(messageId: string): Promise<boolean> {
  const i = assistant.messages.findIndex((m) => m.id === messageId);
  const prev = i > 0 ? assistant.messages[i - 1] : undefined;
  if (!prev || prev.role !== 'user' || !prev.text) return false;
  return send(prev.text);
}

export async function renameConversation(id: string, title: string): Promise<void> {
  const t = title.trim();
  if (!t) return;
  try {
    const r = await conversationsApi.rename(id, t);
    assistant.conversations = assistant.conversations.map((c) => (c.id === id ? { ...c, ...r.conversation } : c));
  } catch (e) {
    assistant.error = errorMessage(e);
  }
}

/** Deletes a conversation; the current one is replaced by a new one. */
export async function deleteConversation(id: string): Promise<void> {
  try {
    await conversationsApi.remove(id);
  } catch (e) {
    assistant.error = errorMessage(e);
    return;
  }
  assistant.conversations = assistant.conversations.filter((c) => c.id !== id);
  if (assistant.currentId === id) newConversation();
}

export function openPanel(): void {
  assistant.panelOpen = true;
  assistant.focus++;
}

export function closePanel(): void {
  assistant.panelOpen = false;
}

export function togglePanel(): void {
  if (assistant.panelOpen) closePanel();
  else openPanel();
}

/** For tests: forgets everything. */
export function resetAssistant(): void {
  stopPolling();
  epoch++;
  viewers = 0;
  fails = 0;
  watching.clear();
  Object.assign(assistant, { conversations: [], currentId: '', messages: [], draft: '', loaded: false, loading: false, sending: false, error: '', panelOpen: false, focus: 0 });
}
