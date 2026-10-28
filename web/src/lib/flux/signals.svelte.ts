// Invalidation signals: what an event says changed, as a counter per thing. A view that shows a change, a node, a
// branch… reads `stamp(key)` in the `$effect` that loads it, and loads again when it moves — no `reload++`, no
// refetch written after each mutation. Keys are built by the helpers below.
import { SvelteMap } from 'svelte/reactivity';

const stamps = new SvelteMap<string, number>();
/** moves when everything must be read again (the stream could not vouch for what it missed) */
let generation = $state(0);

/** the keys a view or a store ever read: a key nobody reads is not counted (an import writes thousands of nodes) */
const read = new Set<string>();

/** Reactive: moves each time `touch(key)` or `touchAll()` is called. 0 until then. */
export function stamp(key: string): number {
  read.add(key);
  return (stamps.get(key) ?? 0) + generation;
}

/** Everything is stale: the stream restarted after a gap, or the session changed. */
export function touchAll(): void {
  generation++;
}

/** the keys waiting for their touch, per delay */
const pending = new Map<number, Set<string>>();

/**
 * `touch` once per `ms` at most: the first touch of a burst waits `ms`, those that come meanwhile join it. A burst
 * (a change applying writes dozens of nodes) moves a key once, and a key touched without pause (an import running for
 * minutes) still moves every `ms`, so what shows it follows the work while it runs.
 */
export function touchSoon(key: string, ms = 250): void {
  if (!read.has(key)) return;
  let keys = pending.get(ms);
  if (!keys) {
    keys = new Set();
    pending.set(ms, keys);
    setTimeout(() => {
      pending.delete(ms);
      for (const k of keys!) touch(k);
    }, ms);
  }
  keys.add(key);
}

/** Says something changed (reducers call it, from the events). */
export function touch(key: string): void {
  if (!read.has(key)) return;
  stamps.set(key, (stamps.get(key) ?? 0) + 1);
}

export const keyOf = {
  change: (id: string) => `change:${id}`,
  node: (id: string) => `node:${id}`,
  /** a branch of the graph moved (a commit was created on it) */
  branch: (name: string) => `branch:${name}`,
  /** a node of the namespace was written (organisation, platform, alm…) */
  namespace: (ns: string) => `ns:${ns}`,
  /** any commit anywhere */
  commits: 'commits',
  /** any change, any status: the list changed */
  changes: 'changes',
  methodology: (name: string) => `methodology:${name}`,
  methodologies: 'methodologies',
  domain: (name: string) => `domain:${name}`,
  domains: 'domains',
  /** a conversation of the caller (ADR 0085): its header or one of its messages */
  conversation: (id: string) => `conversation:${id}`,
  /** the caller's list of conversations: one was created, renamed or deleted */
  conversations: 'conversations',
  /** a job of the caller (ADR 0149): created, started, ended */
  job: (id: string) => `job:${id}`,
  /** the caller's jobs: one was created, started or ended */
  jobs: 'jobs',
  /** the ledger of the model calls attributed to the caller (ADR 0089, 0114): a call was recorded */
  usage: 'usage',
  /** the connectors of the MCP hub: one registered, was lost or came back */
  connectors: 'connectors',
  /** the caller's preferences, written by another tab or device (ADR 0038) */
  preferences: 'preferences',
};

/**
 * Runs `fn` at most once per `ms`: at once when the last run is old enough, else once more when the interval ends (the
 * calls in between collapse into that one). For a read that is large and that an event stream asks for again and again
 * (the list of every change while an agent journals dozens of entries a second).
 */
export function throttled(fn: () => void, ms: number, now: () => number = Date.now): { call: () => void; cancel: () => void } {
  let last = -Infinity;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const run = () => {
    timer = undefined;
    last = now();
    fn();
  };
  return {
    call() {
      if (timer) return;
      const wait = last + ms - now();
      if (wait <= 0) run();
      else timer = setTimeout(run, wait);
    },
    cancel() {
      if (timer) clearTimeout(timer);
      timer = undefined;
    },
  };
}
