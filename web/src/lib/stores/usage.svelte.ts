// The feed of LLM calls: the gateway's ledger (ADR 0089), the one source of every per-call token view (the Tokens
// console, the Token usage pane). Initial load = the latest rows, then a cursor follow (`ListUsage(afterSeq)`) each time
// the event stream says a call was recorded for the caller (`model.call`, key `usage`); while the stream is down it
// polls instead. It runs only while a view is attached and the page visible.
import { untrack } from 'svelte';
import { errorMessage, int, models, type LLMCall, type UsageFilter } from '../api';
import { can, me } from './session.svelte';
import { stream } from '../flux/events.svelte';
import { keyOf, stamp } from '../flux/signals.svelte';

export const POLL_MS = 2000;
const POLL_MAX_MS = 15000;
/** the poll while the event stream is open: a safety net, the events say when to read */
export const FOLLOW_MS = 60000;
/** rows asked by the first load and by every follow */
export const PAGE = 500;
/** rows kept: the oldest are dropped */
export const MAX_ROWS = 2000;

export const usage = $state({
  rows: [] as LLMCall[],
  error: '',
  loading: false,
  /** the whole platform (administrators only) rather than the caller's own calls */
  platform: false,
  /** counts the batches of new rows: views that aggregate server side reload on it */
  version: 0,
});

let viewers = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
let inflight: AbortController | undefined;
let fails = 0;
/** an event came while a read was in flight: read again once it is done */
let again = false;
/** the effect following the events, while a view is attached */
let following: (() => void) | undefined;
/** the last seq seen: the cursor of the follow (0: nothing loaded yet) */
let cursor = 0;
/** rows at or below it were cleared locally (the ledger keeps them) */
let cleared = 0;
/** one token per scope: an answer that comes back for an older one is dropped */
let epoch = 0;

/** What the feed asks for: the calls attributed to the caller (their own and those made on their behalf, ADR 0114), or
 * (administrators) the whole platform. */
export function usageScope(): UsageFilter {
  return usage.platform && can.administer ? {} : { endUser: me() };
}

const seqOf = (c: LLMCall): number => int(c.seq);

function stop(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  inflight?.abort();
  inflight = undefined;
}

function schedule(delay?: number): void {
  if (timer || viewers <= 0) return;
  timer = setTimeout(
    () => {
      timer = undefined;
      void poll();
    },
    delay ?? (stream.status === 'open' && !fails ? FOLLOW_MS : Math.min(POLL_MS * 2 ** fails, POLL_MAX_MS)),
  );
}

/** A call was recorded (the event stream): read what follows the cursor now. */
function onRecorded(): void {
  if (viewers <= 0) return;
  if (inflight) {
    again = true;
    return;
  }
  if (timer) clearTimeout(timer);
  timer = undefined;
  void poll();
}

/** Follows the `usage` stamp while a view is attached. */
function follow(): () => void {
  let seen = -1;
  return $effect.root(() => {
    $effect(() => {
      const at = stamp(keyOf.usage);
      const moved = seen >= 0 && at > seen;
      seen = at;
      if (moved) untrack(onRecorded);
    });
  });
}

function append(calls: LLMCall[]): void {
  const fresh = calls.filter((c) => seqOf(c) > Math.max(cursor, cleared));
  if (!fresh.length) return;
  const merged = [...usage.rows, ...fresh];
  usage.rows = merged.length > MAX_ROWS ? merged.slice(merged.length - MAX_ROWS) : merged;
  usage.version++;
}

async function poll(): Promise<void> {
  if (viewers <= 0) return;
  // nothing is fetched while the page is hidden
  if (typeof document !== 'undefined' && document.hidden) return schedule();
  const mine = epoch;
  const ctrl = new AbortController();
  inflight = ctrl;
  usage.loading = cursor === 0;
  try {
    const res = await models.listUsage({ ...usageScope(), afterSeq: cursor ? String(cursor) : undefined, limit: PAGE }, ctrl.signal);
    if (mine !== epoch || ctrl.signal.aborted) return;
    append(res.calls ?? []);
    cursor = Math.max(cursor, int(res.nextSeq), ...(res.calls ?? []).map(seqOf));
    usage.error = '';
    fails = 0;
    // a backlog is read without waiting, so is a call recorded meanwhile
    schedule(res.hasMore || again ? 0 : undefined);
    again = false;
  } catch (e) {
    if (mine !== epoch || ctrl.signal.aborted) return;
    usage.error = errorMessage(e);
    fails++;
    schedule();
  } finally {
    if (inflight === ctrl) {
      inflight = undefined;
      usage.loading = false;
    }
  }
}

function reload(): void {
  stop();
  epoch++;
  cursor = 0;
  fails = 0;
  usage.rows = [];
  usage.error = '';
  if (viewers > 0) void poll();
}

/** A view of the feed is shown: it loads and follows while there is at least one. */
export function attachUsage(): () => void {
  // called from an $effect: what the first poll reads must not become a dependency of that effect
  untrack(() => {
    viewers++;
    if (viewers === 1) {
      fails = 0;
      following = follow();
      void poll();
    }
  });
  let released = false;
  return () => {
    if (released) return;
    released = true;
    viewers = Math.max(0, viewers - 1);
    if (viewers === 0) {
      stop();
      following?.();
      following = undefined;
    }
  };
}

/** Switches between the caller's own calls and the whole platform (administrators). */
export function setUsagePlatform(platform: boolean): void {
  if (usage.platform === platform) return;
  usage.platform = platform;
  cleared = 0;
  reload();
}

/** Hides the rows seen so far (locally: the ledger keeps them). */
export function clearUsage(): void {
  cleared = Math.max(cleared, cursor, ...usage.rows.map(seqOf));
  usage.rows = [];
}

/** Forgets everything (tests, sign-out). */
export function resetUsage(): void {
  stop();
  following?.();
  following = undefined;
  again = false;
  epoch++;
  viewers = 0;
  cursor = 0;
  cleared = 0;
  fails = 0;
  usage.rows = [];
  usage.error = '';
  usage.loading = false;
  usage.platform = false;
  usage.version = 0;
}
