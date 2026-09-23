// Notifications derived from the WatchEvents stream for the current user's
// processes (all of them when anonymous): completion, failure, stuck,
// pending input or approval, clarification, started by a trigger.
import { onLiveEvent } from './live.svelte';
import { me, hasAnyRole } from './session.svelte';
import { notify } from '../shell/workbench.svelte';
import { loadRaw, save } from '../shell/storage';
import { shortId, type Process, type WatchEvent } from '../api';

export interface Notice {
  id: string;
  /** deduplication key (process + situation) */
  key: string;
  time: string;
  tone: 'ok' | 'error' | 'warn' | 'info';
  title: string;
  text: string;
  processId: string;
  read: boolean;
}

const KEY = 'goap.ide.notifications';
const MAX = 100;

function restore(): Notice[] {
  const raw = loadRaw(KEY);
  return Array.isArray(raw) ? (raw as Notice[]).filter((n) => n && typeof n.id === 'string').slice(0, MAX) : [];
}

class Notifications {
  items = $state<Notice[]>(restore());
  unread = $derived(this.items.filter((n) => !n.read).length);
}

export const notifications = new Notifications();

$effect.root(() => {
  $effect(() => {
    save(KEY, $state.snapshot(notifications.items));
  });
});

export function markRead(id: string): void {
  const n = notifications.items.find((x) => x.id === id);
  if (n) n.read = true;
}

export function markAllRead(): void {
  for (const n of notifications.items) n.read = true;
}

export function clearNotifications(): void {
  notifications.items = [];
}

function label(p: Process): string {
  return p.title || p.agent || p.goal || shortId(p.id);
}

function mine(p: Process): boolean {
  const subject = me();
  return !subject || p.initiator?.subject === subject;
}

function push(n: Omit<Notice, 'id' | 'read' | 'time'> & { time?: string }, toast = false): void {
  if (notifications.items.some((x) => x.key === n.key)) return;
  const notice: Notice = {
    ...n,
    id: `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`,
    time: n.time || new Date().toISOString(),
    read: false,
  };
  notifications.items = [notice, ...notifications.items].slice(0, MAX);
  if (toast) notify(`${n.title} — ${n.text}`, n.tone === 'error' ? 'error' : 'info', 6000);
}

export function noticeFromEvent(e: WatchEvent): void {
  const p = e.process;
  if (!p?.id) return;
  const base = { processId: p.id, time: e.time };
  if (e.type === 'started' && p.trigger) {
    push({ ...base, key: `${p.id}|trigger`, tone: 'info', title: 'Trigger', text: `"${p.trigger}" started ${label(p)}` });
  }
  const approval = p.status === 'waiting' && p.pending?.kind === 'approval';
  if (!mine(p) && !(approval && hasAnyRole('approver', 'admin'))) return;
  switch (e.type) {
    case 'completed':
      push({ ...base, key: `${p.id}|completed`, tone: 'ok', title: 'Run completed', text: label(p) });
      break;
    case 'failed':
      push({ ...base, key: `${p.id}|failed`, tone: 'error', title: 'Run failed', text: `${label(p)}${p.error ? `: ${p.error}` : ''}` }, true);
      break;
    case 'stuck':
      push({ ...base, key: `${p.id}|stuck|${p.steps?.length ?? 0}`, tone: 'warn', title: 'Run stuck', text: label(p) });
      break;
    case 'waiting': {
      const t = p.pending;
      if (t?.kind === 'input')
        push({ ...base, key: `${p.id}|input|${t.step}|${t.action}`, tone: 'warn', title: 'Input expected', text: `${label(p)} — ${t.action ?? ''}` });
      else if (t?.kind === 'approval')
        push(
          { ...base, key: `${p.id}|approval|${t.step}|${t.action}`, tone: 'warn', title: 'Approval required', text: `${label(p)} — ${t.action ?? ''} (${t.permission ?? ''})` },
          true,
        );
      break;
    }
    case 'intent':
      if (p.status === 'clarifying' && p.question)
        push({ ...base, key: `${p.id}|clarify|${p.turns?.length ?? 0}`, tone: 'info', title: 'Clarification question', text: p.question });
      break;
  }
}

onLiveEvent(noticeFromEvent);
