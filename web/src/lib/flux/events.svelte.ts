// The one event stream of the platform (ADR 0053): every fact the services publish arrives here as a thin,
// ordered event. Reducers (stores, queries) react to it; nothing refetches on its own initiative any more.
// A stream cut short resumes where it stopped (epoch + seq); when the server cannot cover the gap it says
// `resync`, and whoever holds state treats it as stale.
import type { LogLine, Process, WatchEvent } from '../api';
import { errorMessage, onTokenChange } from '../api';
import { watchStream, type StreamStatus } from '../stream';

export const EVENT_SERVICE = 'goap.events.v1.EventService';

/** Where a tab looks, as the presence events say. */
export interface Presence {
  tabId: string;
  subject: string;
  kind: string;
  id: string;
  since?: string;
}

/** What the server sends (Connect JSON: uint64 and int64 travel as strings). */
export interface WireEvent {
  epoch?: string;
  seq?: string | number;
  type?: string;
  kind?: string;
  id?: string;
  namespace?: string;
  branch?: string;
  project?: string;
  changeId?: string;
  version?: string | number;
  actor?: string;
  commandId?: string;
  time?: string;
  label?: string;
  process?: Process;
  log?: LogLine;
  presence?: Presence[];
}

/** A fact of the platform. `type` is `<kind>.<what>`: `node.written`, `change.applied`, `process.step`… */
export interface PlatformEvent {
  type: string;
  kind: string;
  id: string;
  namespace: string;
  branch: string;
  project: string;
  changeId: string;
  version: number;
  actor: string;
  /** the id the issuing client gave its command (`X-Goap-Command`): a client knows the echo of its own writes */
  commandId: string;
  time: string;
  /** version label of a methodology or domain */
  label: string;
  process?: Process;
  log?: LogLine;
  presence: Presence[];
}

class EventStream {
  status = $state<StreamStatus>('stopped');
  error = $state('');
  /** seq of the last event seen, within `epoch` */
  seq = 0;
  epoch = '';
}

export const stream = new EventStream();

type Listener = (e: PlatformEvent) => void;
const listeners = new Set<Listener>();
const resyncListeners = new Set<() => void>();

/** Calls `fn` for every event of the stream (presence and housekeeping excluded: see `onPresence`, `onResync`). */
export function onPlatformEvent(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Calls `fn` for the events of one kind (`node`, `change`, `baseline`, `process`, `methodology`, `domain`). */
export function onKind(kind: string | string[], fn: Listener): () => void {
  const kinds = Array.isArray(kind) ? kind : [kind];
  return onPlatformEvent((e) => {
    if (kinds.includes(e.kind)) fn(e);
  });
}

/**
 * Calls `fn` when what the client holds can no longer be trusted: the stream restarted after a gap the server
 * could not cover, or the session changed. Whoever caches data marks it stale there. Returns the unsubscribe.
 */
export function onResync(fn: () => void): () => void {
  resyncListeners.add(fn);
  return () => resyncListeners.delete(fn);
}

const presenceListeners = new Set<(type: string, presence: Presence[]) => void>();

/** Calls `fn` for the presence events: `presence.snapshot` (all the tabs), `.joined`, `.moved`, `.left` (the one tab). */
export function onPresence(fn: (type: string, presence: Presence[]) => void): () => void {
  presenceListeners.add(fn);
  return () => presenceListeners.delete(fn);
}

function toEvent(w: WireEvent): PlatformEvent {
  return {
    type: w.type ?? '',
    kind: w.kind ?? '',
    id: w.id ?? '',
    namespace: w.namespace ?? '',
    branch: w.branch ?? '',
    project: w.project ?? '',
    changeId: w.changeId ?? '',
    version: Number(w.version ?? 0),
    actor: w.actor ?? '',
    commandId: w.commandId ?? '',
    time: w.time ?? '',
    label: w.label ?? '',
    process: w.process,
    log: w.log,
    presence: w.presence ?? [],
  };
}

/** The legacy shape of a process event (`started`, `step`, `log`…), for what still reads WatchEvent. */
export function toWatchEvent(e: PlatformEvent): WatchEvent {
  return { type: e.type.replace(/^process\./, ''), time: e.time, process: e.process, log: e.log };
}

function resync(): void {
  for (const fn of resyncListeners) fn();
}

/** Takes one message of the stream (exported for tests). */
export function receive(w: WireEvent): void {
  const seq = Number(w.seq ?? 0);
  switch (w.type) {
    case 'heartbeat':
      // the server numbered events this client never saw: nothing it holds can be trusted
      if (w.epoch === stream.epoch && seq > stream.seq) {
        stream.seq = seq;
        resync();
      }
      return;
    case 'resync':
      if (w.epoch) stream.epoch = w.epoch;
      stream.seq = 0;
      resync();
      return;
  }
  if (w.epoch) {
    if (stream.epoch && w.epoch !== stream.epoch) stream.seq = 0; // another stream: seqs no longer compare
    stream.epoch = w.epoch;
  }
  if (seq > 0) stream.seq = seq;
  const e = toEvent(w);
  if (e.kind === 'presence') {
    for (const fn of presenceListeners) fn(e.type, e.presence);
    return;
  }
  for (const fn of listeners) fn(e);
}

/** Forgets where the stream was (a new identity, tests). */
export function resetStream(): void {
  stream.epoch = '';
  stream.seq = 0;
}

let stop: (() => void) | undefined;

/** Starts (or restarts, on every token change: the identity decides what is visible) the stream. */
export function startEvents(): () => void {
  const start = () => {
    stop?.();
    stop = watchStream<{ epoch?: string; afterSeq?: number }, WireEvent>({
      service: EVENT_SERVICE,
      method: 'Watch',
      body: () => (stream.epoch ? { epoch: stream.epoch, afterSeq: stream.seq } : {}),
      onMessage: receive,
      onStatus: (s, err) => {
        stream.status = s;
        if (s === 'open') stream.error = '';
        else if (err) stream.error = errorMessage(err);
      },
    });
  };
  start();
  const off = onTokenChange(() => {
    // another identity: the history of this one means nothing to it
    stream.epoch = '';
    stream.seq = 0;
    start();
    resync();
  });
  return () => {
    off();
    stop?.();
    stop = undefined;
  };
}

/**
 * The events of one process and of the processes it started, from the shared stream. `onStatus` follows the
 * stream's own. Replaces a stream of its own per run.
 */
export function watchProcess(processId: string, onEvent: (e: WatchEvent) => void, onStatus?: (s: StreamStatus) => void): () => void {
  const off = onKind('process', (e) => {
    const related = e.id === processId || e.process?.parentId === processId;
    if (related) onEvent(toWatchEvent(e));
  });
  onStatus?.(stream.status);
  const stopStatus = $effect.root(() => {
    $effect(() => onStatus?.(stream.status));
  });
  return () => {
    off();
    stopStatus();
  };
}
