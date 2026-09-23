// Conversational assistant: conversation history (localStorage), info from
// published methodologies (agent and action descriptions), and sending
// requests (StartProcess without a methodology).
import { SvelteMap } from 'svelte/reactivity';
import { engine, registry, errorMessage, type Agent, type Methodology } from '../api';
import { loadRaw, save } from '../shell/storage';
import { ingestProcess } from './live.svelte';
import { baselines, refreshBaselines, latestPublished, methodologies, refreshMethodologies } from './catalog.svelte';

export interface Thread {
  id: string;
  /** user's request */
  text: string;
  at: string;
  processId?: string;
  error?: string;
}

interface Conversation {
  threads: Thread[];
  /** chosen baseline ('': the most recent) */
  baselineId: string;
  /** draft of the input box */
  draft: string;
}

const KEY = 'goap.ide.assistant';

function restore(): Conversation {
  const raw = loadRaw(KEY) as Partial<Conversation> | undefined;
  return {
    threads: Array.isArray(raw?.threads) ? raw.threads.filter((t) => t && typeof t.id === 'string') : [],
    baselineId: typeof raw?.baselineId === 'string' ? raw.baselineId : '',
    draft: typeof raw?.draft === 'string' ? raw.draft : '',
  };
}

export const conversation: Conversation = $state(restore());
export const assistantUi = $state({ sending: false, focus: 0 });

$effect.root(() => {
  $effect(() => {
    save(KEY, $state.snapshot(conversation));
  });
});

// --- published methodologies -------------------------------------------------------------

/** Latest published version of each methodology, by name. */
export const published = new SvelteMap<string, Methodology>();
const pending = new Map<string, Promise<void>>();

export function loadMethodology(name: string): Promise<void> {
  if (!name || published.has(name)) return Promise.resolve();
  let p = pending.get(name);
  if (!p) {
    p = registry
      .getMethodology(name, '')
      .then((r) => {
        if (r.methodology) published.set(name, r.methodology);
      })
      .catch(() => undefined)
      .finally(() => pending.delete(name));
    pending.set(name, p);
  }
  return p;
}

export async function loadCatalog(): Promise<void> {
  if (!methodologies.loaded) await refreshMethodologies();
  await Promise.all(latestPublished().map((m) => loadMethodology(m.name ?? '')));
}

export interface AgentCard {
  methodology: string;
  agent: Agent;
  /** implicit agent of a methodology without agents */
  implicit: boolean;
  examples: string[];
}

export function agentCards(): AgentCard[] {
  const out: AgentCard[] = [];
  for (const s of latestPublished()) {
    const m = published.get(s.name ?? '');
    if (!m) continue;
    const goalExamples = (m.goals ?? []).flatMap((g) => g.examples ?? []);
    if (m.agents?.length) {
      for (const a of m.agents) {
        const goals = (m.goals ?? []).filter((g) => !a.goals?.length || a.goals.includes(g.name ?? ''));
        const ex = a.examples?.length ? a.examples : goals.flatMap((g) => g.examples ?? []);
        out.push({ methodology: m.name ?? '', agent: a, implicit: false, examples: ex.slice(0, 4) });
      }
    } else {
      out.push({
        methodology: m.name ?? '',
        agent: { name: 'default', description: m.description, examples: [] },
        implicit: true,
        examples: goalExamples.slice(0, 4),
      });
    }
  }
  return out;
}

/** Readable name of an agent: first short sentence of its description, else its name. */
export function agentLabel(methodology: string | undefined, agent: string | undefined): string {
  const m = published.get(methodology ?? '');
  const a = m?.agents?.find((x) => x.name === agent);
  const desc = (a?.description || (agent === 'default' || !agent ? m?.description : '') || '').trim();
  const first = desc.split(/(?<=[.!?])\s|\n| — | : /)[0]?.replace(/[.:]$/, '').trim() ?? '';
  if (first && first.length <= 60) return first;
  if (agent && agent !== 'default') return agent;
  return methodology || 'assistant';
}

/** Label of an action: its description in the methodology, else its name made readable. */
export function actionLabel(methodology: string | undefined, action: string | undefined): string {
  const m = published.get(methodology ?? '');
  const a = m?.actions?.find((x) => x.name === action);
  if (a?.description) return a.description;
  return (action ?? '').replace(/[_-]+/g, ' ');
}

export function goalLabel(methodology: string | undefined, goal: string | undefined): string {
  const m = published.get(methodology ?? '');
  return m?.goals?.find((g) => g.name === goal)?.description || (goal ?? '').replace(/[_-]+/g, ' ');
}

// --- sending -----------------------------------------------------------------------------------

export async function latestBaselineId(): Promise<string> {
  if (!baselines.loaded) await refreshBaselines();
  const sorted = [...baselines.items].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? ''));
  return sorted[sorted.length - 1]?.id ?? '';
}

export async function sendRequest(text: string): Promise<void> {
  const t = text.trim();
  if (!t || assistantUi.sending) return;
  assistantUi.sending = true;
  const thread: Thread = { id: `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`, text: t, at: new Date().toISOString() };
  conversation.threads.push(thread);
  const th = conversation.threads[conversation.threads.length - 1];
  conversation.draft = '';
  try {
    const baselineId = conversation.baselineId || (await latestBaselineId());
    const res = await engine.startProcess({ intent: t, ...(baselineId ? { baselineId } : {}) });
    if (res.process?.id) {
      ingestProcess(res.process);
      th.processId = res.process.id;
      void loadMethodology(res.process.methodology ?? '');
    } else th.error = 'The engine did not create a run.';
  } catch (e) {
    th.error = errorMessage(e);
  } finally {
    assistantUi.sending = false;
  }
}

export function newConversation(): void {
  conversation.threads = [];
  conversation.draft = '';
}
