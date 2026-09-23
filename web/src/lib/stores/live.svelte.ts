// Live workbench data: global WatchEvents stream ("Events" console), known
// processes, log lines and LLM calls ("Tokens" console). Every process
// source (stream, GetProcess, ListProcesses) goes through `ingestProcess`.
import { SvelteMap } from 'svelte/reactivity';
import {
  engine,
  errorMessage,
  int,
  onTokenChange,
  type LogLine,
  type Process,
  type WatchEvent,
} from '../api';
import { watchEvents, type StreamStatus } from '../stream';

export interface EventRow {
  seq: number;
  type: string;
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

export interface TokenRow {
  key: string;
  time: string;
  processId: string;
  agent: string;
  action: string;
  step: number;
  provider: string;
  model: string;
  input: number;
  output: number;
  durationMs: number;
  error: string;
}

const MAX_EVENTS = 1000;
const MAX_LOGS = 3000;
const MAX_TOKENS = 5000;

class Live {
  status = $state<StreamStatus>('stopped');
  error = $state('');
  /** pauses the event display (the stream keeps running) */
  paused = $state(false);
  events = $state.raw<EventRow[]>([]);
  logs = $state.raw<LogRow[]>([]);
  tokens = $state.raw<TokenRow[]>([]);
  processesLoading = $state(false);
  processesError = $state('');
  processesLoaded = $state(false);
}

export const live = new Live();

/** Known processes, by id. */
export const processes = new SvelteMap<string, Process>();

const logKeys = new Set<string>();
const tokenKeys = new Set<string>();
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

/** Records a process state (and its logs / LLM calls). */
export function ingestProcess(p: Process | undefined): void {
  if (!p?.id) return;
  processes.set(p.id, p);
  const fresh: TokenRow[] = [];
  const lines: LogLine[] = [];
  for (const s of p.steps ?? []) {
    const idx = s.index ?? 0;
    (s.llmCalls ?? []).forEach((c, i) => {
      const key = `${p.id}|${idx}|${i}`;
      if (tokenKeys.has(key)) return;
      tokenKeys.add(key);
      fresh.push({
        key,
        time: s.endedAt || s.startedAt || p.updatedAt || '',
        processId: p.id ?? '',
        agent: p.agent ?? '',
        action: s.action ?? '',
        step: idx,
        provider: c.provider ?? '',
        model: c.model ?? '',
        input: int(c.inputTokens),
        output: int(c.outputTokens),
        durationMs: int(c.durationMs),
        error: c.error ?? '',
      });
    });
    for (const l of s.logs ?? []) lines.push({ ...l, processId: l.processId || p.id, action: l.action || s.action, step: l.step ?? idx });
  }
  if (fresh.length) live.tokens = cap([...live.tokens, ...fresh], MAX_TOKENS);
  if (lines.length) addLogs(lines, p.id);
}

function describe(e: WatchEvent): string {
  if (e.type === 'log') {
    const l = e.log;
    return [l?.step !== undefined && l.action ? `#${(l.step ?? 0) + 1}` : '', l?.action ?? ''].filter(Boolean).join(' ');
  }
  const p = e.process;
  if (!p) return '';
  if (e.type === 'waiting' && p.pending) return `${p.pending.kind ?? 'input'} · ${p.pending.action ?? ''}`;
  if (e.type === 'intent') return p.question ? 'clarification' : p.goal ? `goal ${p.goal}` : '';
  const last = p.steps?.[p.steps.length - 1];
  if (last && (e.type === 'step' || e.type === 'failed' || e.type === 'stuck'))
    return `#${(last.index ?? p.steps!.length - 1) + 1} ${last.action ?? ''}`;
  return p.goal ? `goal ${p.goal}` : '';
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
  const row: EventRow = {
    seq: ++seq,
    type: e.type ?? '',
    time: e.time || e.log?.time || new Date().toISOString(),
    processId: pid,
    agent: p?.agent ?? '',
    methodology: p?.methodology ?? '',
    detail: describe(e),
    level: e.log?.level,
  };
  live.events = cap([...live.events, row], MAX_EVENTS);
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
  live.events = [];
}

export function clearLogs(): void {
  live.logs = [];
  logKeys.clear();
}

export function clearTokens(): void {
  live.tokens = [];
  tokenKeys.clear();
}

let stop: (() => void) | undefined;

/** Starts (or restarts) the global stream; relaunched on every token change. */
export function startLive(): () => void {
  const start = () => {
    stop?.();
    stop = watchEvents({
      onEvent: (e) => ingestEvent(e),
      onStatus: (s, err) => {
        live.status = s;
        if (s === 'open') live.error = '';
        else if (err) live.error = errorMessage(err);
      },
    });
  };
  start();
  const off = onTokenChange(() => {
    start();
    void refreshProcesses();
  });
  return () => {
    off();
    stop?.();
    stop = undefined;
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
