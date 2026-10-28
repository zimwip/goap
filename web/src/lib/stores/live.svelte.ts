// Live workbench data: the platform event stream as the "Events" console shows it (every kind: processes, changes,
// nodes, commits, jobs, model calls…), known processes and log lines. (The LLM calls are the gateway's ledger: `usage.svelte.ts`.) Every process
// source (stream, GetProcess, ListProcesses) goes through `ingestProcess`.
import { SvelteMap } from 'svelte/reactivity';
import {
  engine,
  errorMessage,
  type LogLine,
  type Process,
  type WatchEvent,
} from '../api';
import type { StreamStatus } from '../stream';
import { onKind, onPlatformEvent, onResync, startEvents, stream, toWatchEvent, type PlatformEvent } from '../flux/events.svelte';

export interface EventRow {
  seq: number;
  /** the kind of the event: `process`, `change`, `node`, `commit`, `job`, `model`… */
  kind: string;
  type: string;
  /** what the event is about (a change, a node, a job…; the process of a process event) */
  id: string;
  /** the change it happened in, when there is one */
  changeId: string;
  /** who did it */
  actor: string;
  time: string;
  processId: string;
  agent: string;
  methodology: string;
  /** step / action concerned */
  detail: string;
  level?: string;
}

export interface LogRow {
  key: string;
  time: string;
  level: string;
  message: string;
  processId: string;
  action: string;
  step?: number;
}

const MAX_EVENTS = 1000;
/** rows are added to the console at most this often: an import sends thousands of events */
const FLUSH_MS = 100;
const MAX_LOGS = 3000;

class Live {
  status = $state<StreamStatus>('stopped');
  error = $state('');
  /** pauses the event display (the stream keeps running) */
  paused = $state(false);
  events = $state.raw<EventRow[]>([]);
  logs = $state.raw<LogRow[]>([]);
  processesLoading = $state(false);
  processesError = $state('');
  processesLoaded = $state(false);
}

export const live = new Live();

/** Known processes, by id. */
export const processes = new SvelteMap<string, Process>();

const logKeys = new Set<string>();
let seq = 0;

function cap<T>(list: T[], max: number): T[] {
  return list.length > max ? list.slice(list.length - max) : list;
}

function addLogs(lines: LogLine[], fallbackProcess = ''): void {
  const fresh: LogRow[] = [];
  for (const l of lines) {
    const processId = l.processId || fallbackProcess;
    const key = `${processId}|${l.time ?? ''}|${l.step ?? ''}|${l.message ?? ''}`;
    if (logKeys.has(key)) continue;
    logKeys.add(key);
    fresh.push({
      key,
      time: l.time ?? '',
      level: l.level || 'info',
      message: l.message ?? '',
      processId,
      action: l.action ?? '',
      step: l.step,
    });
  }
  if (!fresh.length) return;
  const merged = [...live.logs, ...fresh];
  // Step logs may arrive after live lines: sort by date.
  merged.sort((a, b) => a.time.localeCompare(b.time));
  live.logs = cap(merged, MAX_LOGS);
}

/** Records a process state (and its logs). */
export function ingestProcess(p: Process | undefined): void {
  if (!p?.id) return;
  processes.set(p.id, p);
  const lines: LogLine[] = [];
  for (const s of p.steps ?? []) {
    const idx = s.index ?? 0;
    for (const l of s.logs ?? []) lines.push({ ...l, processId: l.processId || p.id, action: l.action || s.action, step: l.step ?? idx });
  }
  if (lines.length) addLogs(lines, p.id);
}

function describe(e: WatchEvent): string {
  if (e.type === 'log') {
    const l = e.log;
    return [l?.step !== undefined && l.action ? `#${(l.step ?? 0) + 1}` : '', l?.action ?? ''].filter(Boolean).join(' ');
  }
  const p = e.process;
  if (!p) return '';
  if (e.type === 'waiting' && p.pending?.kind === 'condition') return `waiting for ${(p.pending.conditions ?? []).join(', ')}`;
  if (e.type === 'waiting' && p.pending) return `${p.pending.kind ?? 'input'} · ${p.pending.action ?? ''}`;
  if (e.type === 'intent') return p.question ? 'clarification' : p.goal ? `goal ${p.goal}` : '';
  const last = p.steps?.[p.steps.length - 1];
  if (last && (e.type === 'step' || e.type === 'failed' || e.type === 'stuck'))
    return `#${(last.index ?? p.steps!.length - 1) + 1} ${last.action ?? ''}`;
  return p.goal ? `goal ${p.goal}` : '';
}

let queued: EventRow[] = [];
let flushTimer: ReturnType<typeof setTimeout> | undefined;

/** Adds rows to the console, a burst at a time. */
function addRow(row: Omit<EventRow, 'seq'>): void {
  queued.push({ ...row, seq: ++seq });
  flushTimer ??= setTimeout(() => {
    flushTimer = undefined;
    const rows = queued;
    queued = [];
    live.events = cap([...live.events, ...rows], MAX_EVENTS);
  }, FLUSH_MS);
}

/** What a row says about an event that is not a process's. */
function describePlatform(e: PlatformEvent): string {
  switch (e.kind) {
    case 'node':
      return [e.namespace, e.version ? `v${e.version}` : '', e.branch && e.branch !== 'main' ? `on ${e.branch}` : ''].filter(Boolean).join(' ');
    case 'commit':
      return e.branch;
    case 'change':
      return [e.project, e.namespace].filter(Boolean).join(' · ');
    case 'job':
      return [e.label, e.namespace].filter(Boolean).join(' · ');
    case 'model':
    case 'methodology':
    case 'domain':
    case 'connector':
    case 'preferences':
      return e.label;
    default:
      return e.label || e.namespace;
  }
}

/** Records an event of the stream that is not a process's (those go through `ingestEvent`). */
function recordPlatform(e: PlatformEvent): void {
  if (e.kind === 'process') return;
  addRow({
    kind: e.kind,
    type: e.type,
    id: e.id,
    changeId: e.changeId,
    actor: e.actor,
    time: e.time || new Date().toISOString(),
    processId: '',
    agent: '',
    methodology: '',
    detail: describePlatform(e),
  });
}

const eventListeners = new Set<(e: WatchEvent) => void>();

/** Subscription to global stream events (notifications…). */
export function onLiveEvent(fn: (e: WatchEvent) => void): () => void {
  eventListeners.add(fn);
  return () => eventListeners.delete(fn);
}

/** Processes an event (global stream or a run tab's stream). */
export function ingestEvent(e: WatchEvent, record = true): void {
  if (e.process) ingestProcess(e.process);
  if (e.log) addLogs([e.log]);
  if (!record) return;
  for (const fn of eventListeners) fn(e);
  const pid = e.process?.id || e.log?.processId || '';
  const p = e.process ?? processes.get(pid);
  addRow({
    kind: 'process',
    type: e.type ?? '',
    id: pid,
    changeId: p?.changeId ?? '',
    actor: '',
    time: e.time || e.log?.time || new Date().toISOString(),
    processId: pid,
    agent: p?.agent ?? '',
    methodology: p?.methodology ?? '',
    detail: describe(e),
    level: e.log?.level,
  });
}

export async function refreshProcesses(): Promise<void> {
  live.processesLoading = true;
  try {
    const list = (await engine.listProcesses({})).processes ?? [];
    for (const p of list) ingestProcess(p);
    live.processesError = '';
  } catch (e) {
    live.processesError = errorMessage(e);
  } finally {
    live.processesLoading = false;
    live.processesLoaded = true;
  }
}

export function clearEvents(): void {
  queued = [];
  live.events = [];
}

export function clearLogs(): void {
  live.logs = [];
  logKeys.clear();
}

/**
 * Starts the platform event stream and follows its process events (ADR 0053). The process list is read again when
 * the stream cannot vouch for what was missed (a gap, a new session): nothing polls.
 */
export function startLive(): () => void {
  const stopEvents = startEvents();
  const offProcess = onKind('process', (e) => ingestEvent(toWatchEvent(e)));
  const offOthers = onPlatformEvent(recordPlatform);
  const offResync = onResync(() => void refreshProcesses());
  const stopStatus = $effect.root(() => {
    $effect(() => {
      live.status = stream.status;
      live.error = stream.error;
    });
  });
  void refreshProcesses();
  return () => {
    offProcess();
    offOthers();
    offResync();
    stopStatus();
    stopEvents();
  };
}

/** Known sub-processes of a process (by parentId or via the steps). */
export function childrenOf(p: Process | undefined): string[] {
  if (!p?.id) return [];
  const ids = new Set<string>();
  for (const s of p.steps ?? []) for (const c of s.childProcessIds ?? []) ids.add(c);
  if (p.pending?.childProcessId) ids.add(p.pending.childProcessId);
  for (const q of processes.values()) if (q.parentId === p.id && q.id) ids.add(q.id);
  return [...ids];
}
