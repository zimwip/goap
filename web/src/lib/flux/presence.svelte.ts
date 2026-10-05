// Who is looking at what (ADR 0053): each open page of the web tells the platform which tab it shows, every few
// seconds, and follows the same for everyone else on the event stream. Presence is ephemeral: the server forgets a
// tab that stops saying so, and nothing of it is stored.
import { SvelteMap } from 'svelte/reactivity';
import { BASE, getToken, rpc } from '../api';
import { activeTab } from '../shell/tabs.svelte';
import { me, subjectOfKey } from '../stores/session.svelte';
import { EVENT_SERVICE, onPresence, stream, type Presence } from './events.svelte';

/** One person looking at something. */
export interface Viewer {
  subject: string;
  /** the current user, on another tab or window */
  me: boolean;
  initials: string;
  /** a stable colour per person */
  color: string;
}

const HEARTBEAT_MS = 10_000;

/** This page. */
const TAB_ID = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;

/** Every tab of the platform the caller may see, by `subject/tabId`. */
const tabs = new SvelteMap<string, Presence>();

const keyOfTab = (p: Presence) => `${p.subject}/${p.tabId}`;

export function colorOf(subject: string): string {
  let h = 0;
  for (const c of subject) h = (h * 31 + c.charCodeAt(0)) % 360;
  return `hsl(${h} 55% 45%)`;
}

export function initialsOf(subject: string): string {
  const name = subjectOfKey(subject).split('@')[0];
  const parts = name.split(/[\s._-]+/).filter(Boolean);
  const letters = parts.length > 1 ? parts[0][0] + parts[1][0] : name.slice(0, 2);
  return letters.toUpperCase();
}

function viewerOf(subject: string): Viewer {
  return { subject, me: subject === me(), initials: initialsOf(subject), color: colorOf(subject) };
}

/** The people looking at a tab (`<kind>:<id>`, the id of a tab of the shell), this very page excluded. */
export function viewersOf(tabId: string): Viewer[] {
  const seen = new Set<string>();
  const out: Viewer[] = [];
  for (const p of tabs.values()) {
    if (p.tabId === TAB_ID || `${p.kind}:${p.id}` !== tabId || seen.has(p.subject)) continue;
    seen.add(p.subject);
    out.push(viewerOf(p.subject));
  }
  return out.sort((a, b) => Number(a.me) - Number(b.me) || a.subject.localeCompare(b.subject));
}

/** Everyone online, with the tabs they look at. */
export function onlineUsers(): { viewer: Viewer; tabs: string[] }[] {
  const by = new Map<string, Set<string>>();
  for (const p of tabs.values()) {
    const set = by.get(p.subject) ?? new Set<string>();
    if (p.kind) set.add(`${p.kind}:${p.id}`);
    by.set(p.subject, set);
  }
  return [...by]
    .map(([subject, set]) => ({ viewer: viewerOf(subject), tabs: [...set] }))
    .sort((a, b) => Number(a.viewer.me) - Number(b.viewer.me) || a.viewer.subject.localeCompare(b.viewer.subject));
}

/** Where this page looks: the active tab (kind and the rest of its id). */
function location(): { kind: string; id: string } {
  const t = activeTab();
  return t ? { kind: t.kind, id: t.id.slice(t.kind.length + 1) } : { kind: '', id: '' };
}

async function heartbeat(): Promise<void> {
  if (!getToken() && stream.status !== 'open') return;
  try {
    await rpc(EVENT_SERVICE, 'Heartbeat', { tabId: TAB_ID, ...location() });
  } catch {
    // presence is best effort: the next beat tries again
  }
}

function leave(): void {
  // the page is going: a request that survives it (sendBeacon cannot carry the token)
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  void fetch(`${BASE}/${EVENT_SERVICE}/Leave`, { method: 'POST', headers, body: JSON.stringify({ tabId: TAB_ID }), keepalive: true }).catch(() => {});
}

/** Starts reporting this page's location and following everyone's. Returns the stop function. */
export function startPresence(): () => void {
  const off = onPresence((type, list) => {
    if (type === 'presence.snapshot') {
      tabs.clear();
      for (const p of list) tabs.set(keyOfTab(p), p);
    } else if (type === 'presence.left') {
      for (const p of list) tabs.delete(keyOfTab(p));
    } else {
      for (const p of list) tabs.set(keyOfTab(p), p);
    }
  });
  const stop = $effect.root(() => {
    // each move to another tab says so at once; a page nobody looks at (hidden window) falls silent and expires
    $effect(() => {
      void activeTab()?.id;
      if (stream.status === 'open') void heartbeat();
    });
  });
  const timer = setInterval(() => {
    if (document.visibilityState === 'visible' && stream.status === 'open') void heartbeat();
  }, HEARTBEAT_MS);
  const visible = () => {
    if (document.visibilityState === 'visible') void heartbeat();
  };
  document.addEventListener('visibilitychange', visible);
  window.addEventListener('pagehide', leave);
  return () => {
    off();
    stop();
    clearInterval(timer);
    document.removeEventListener('visibilitychange', visible);
    window.removeEventListener('pagehide', leave);
    leave();
    tabs.clear();
  };
}
