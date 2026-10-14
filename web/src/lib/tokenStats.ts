// Token consumption views over the gateway's ledger of LLM calls (ADR 0089): the pure side (source labels, the
// mapping of `UsageSummary` rows to slices and time buckets, the rule of the prompt link). No process data here.
import { int, type LLMCall, type UsageSummaryRow } from './api';
import type { ModelExchangeRequest } from './shell/modelExchangeState.svelte';

const SOURCES: Record<string, string> = {
  assistant: 'Assistant',
  helper: 'Helper',
  engine: 'Engine',
  indexer: 'Indexer',
  intent: 'Intent',
  embed: 'Embeddings',
  calibration: 'Calibration',
  other: 'Other',
};

/** Readable name of the `source` of a call; an unknown or empty source reads as "Other". */
export function sourceLabel(source: string | undefined): string {
  return SOURCES[source ?? ''] ?? 'Other';
}

/** The sources the console filters by. */
export const SOURCE_IDS = Object.keys(SOURCES);

/** An engine call of a process and a change has its exchange in the change log (the `model.call` entry, ADR 0059). */
export function inChangeLog(c: Pick<LLMCall, 'source' | 'processId' | 'changeId'>): boolean {
  return c.source === 'engine' && !!c.processId && !!c.changeId;
}

/** Whether the prompt of a call can be shown: in the log of its change, or stored by the gateway (`hasExchange`, ADR 0089). */
export function hasExchange(c: Pick<LLMCall, 'source' | 'processId' | 'changeId' | 'hasExchange'>): boolean {
  return inChangeLog(c) || !!c.hasExchange;
}

/** The request that opens the prompt of a call: the log of its change for an engine call of a change, else the gateway by seq; undefined when there is none to read. */
export function exchangeRequestOf(c: LLMCall, label: string): ModelExchangeRequest | undefined {
  if (inChangeLog(c)) return { label, meta: c, changeId: c.changeId ?? '', processId: c.processId ?? '', step: stepOf(c), call: callOf(c) };
  if (c.hasExchange) return { label, meta: c, seq: c.seq };
  return undefined;
}

/** The step of a call, -1 when it belongs to none (proto3 omits a 0). */
export function stepOf(c: Pick<LLMCall, 'processId' | 'step'>): number {
  return c.processId ? (c.step ?? 0) : -1;
}

/** The position of the call in its step. */
export function callOf(c: Pick<LLMCall, 'processId' | 'call'>): number {
  return c.processId ? (c.call ?? 0) : -1;
}

export type Dim = 'model' | 'alias' | 'source' | 'agent' | 'action' | 'subject' | 'end_user' | 'process';

export interface Slice {
  key: string;
  label: string;
  input: number;
  output: number;
  calls: number;
  errors: number;
  total: number;
}

const SYSTEM = 'system:';

/**
 * Who acted, as a person reads it (ADR 0114): a platform service by what it is (`system:trigger:nightly` reads
 * "trigger nightly", `system:companion:sdlc/review` "companion sdlc/review"), `me` as "you, directly".
 */
export function actorLabel(subject: string | undefined, me?: string): string {
  const s = subject ?? '';
  if (me && s === me) return 'you, directly';
  if (!s.startsWith(SYSTEM)) return s || '—';
  const rest = s.slice(SYSTEM.length);
  const i = rest.indexOf(':');
  return i < 0 ? rest : `${rest.slice(0, i)} ${rest.slice(i + 1)}`;
}

/**
 * The service a call was made through on behalf of its end user (ADR 0114): the label of its subject when someone
 * else than the end user called, "" for a call the end user made themselves (or one attributed to nobody).
 */
export function viaOf(c: Pick<LLMCall, 'subject' | 'endUser'>): string {
  return c.endUser && c.subject !== c.endUser ? actorLabel(c.subject) : '';
}

/** The label of a group value: sources read as names, who acted as `actorLabel` (`me`: the caller), an empty value as a dash. */
export function keyLabel(dim: Dim, key: string, me?: string): string {
  if (dim === 'source') return sourceLabel(key);
  if (dim === 'subject') return actorLabel(key, me);
  return key || '—';
}

export function sliceOf(dim: Dim, r: UsageSummaryRow, me?: string): Slice {
  const input = int(r.inputTokens);
  const output = int(r.outputTokens);
  const key = r.key ?? '';
  return { key, label: keyLabel(dim, key, me), input, output, calls: int(r.calls), errors: int(r.errors), total: input + output };
}

/** Rows to slices, the biggest first. */
export function slicesOf(dim: Dim, rows: UsageSummaryRow[] | undefined, me?: string): Slice[] {
  return (rows ?? []).map((r) => sliceOf(dim, r, me)).sort((a, b) => b.total - a.total);
}

export interface Totals {
  input: number;
  output: number;
  total: number;
  calls: number;
  errors: number;
  durationMs: number;
}

/** The grand total of any grouping (every call is in exactly one group). */
export function totalsOf(rows: UsageSummaryRow[] | undefined): Totals {
  const t: Totals = { input: 0, output: 0, total: 0, calls: 0, errors: 0, durationMs: 0 };
  for (const r of rows ?? []) {
    t.input += int(r.inputTokens);
    t.output += int(r.outputTokens);
    t.calls += int(r.calls);
    t.errors += int(r.errors);
    t.durationMs += int(r.durationMs);
  }
  t.total = t.input + t.output;
  return t;
}

export interface Bucket {
  key: string;
  label: string;
  input: number;
  output: number;
  calls: number;
}

const HOUR = 3_600_000;
const DAY = 86_400_000;

/** Start (UTC ms) of a `day` / `hour` key of the summary. */
export function bucketTime(key: string): number {
  return Date.parse(key.length > 10 ? `${key}:00:00Z` : `${key}T00:00:00Z`);
}

/**
 * The series of a `day` / `hour` summary, with the empty periods filled in from `fromMs` (0: the first period
 * with data) to `toMs`. Keys and labels are UTC, as the ledger's.
 */
export function bucketsOf(rows: UsageSummaryRow[] | undefined, hourly: boolean, fromMs: number, toMs: number): Bucket[] {
  const step = hourly ? HOUR : DAY;
  const byTime = new Map<number, UsageSummaryRow>();
  for (const r of rows ?? []) if (r.key) byTime.set(bucketTime(r.key), r);
  if (!byTime.size && !fromMs) return [];
  const first = Math.floor((fromMs || Math.min(...byTime.keys())) / step) * step;
  const count = Math.min(400, Math.max(1, Math.floor((toMs - first) / step) + 1));
  const out: Bucket[] = [];
  for (let i = 0; i < count; i++) {
    const t = first + i * step;
    const r = byTime.get(t);
    const d = new Date(t);
    const iso = d.toISOString();
    out.push({
      key: hourly ? iso.slice(0, 13) : iso.slice(0, 10),
      label: hourly ? `${iso.slice(11, 13)}:00` : d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', timeZone: 'UTC' }),
      input: int(r?.inputTokens),
      output: int(r?.outputTokens),
      calls: int(r?.calls),
    });
  }
  return out;
}

export interface RunSlice extends Slice {
  /** total relative to the average run */
  ratio: number;
}

/**
 * A person's consumption split by who acted (ADR 0114), from a summary of the calls attributed to them grouped by
 * subject: `direct` the calls they made, `indirect` those platform services made on their behalf (triggers, companion
 * runs, their sub-agents), all of them in `all`.
 */
export function directSplit(bySubject: UsageSummaryRow[] | undefined, me: string): { all: Totals; direct: Totals; indirect: Totals } {
  const rows = bySubject ?? [];
  return {
    all: totalsOf(rows),
    direct: totalsOf(rows.filter((r) => (r.key ?? '') === me)),
    indirect: totalsOf(rows.filter((r) => (r.key ?? '') !== me)),
  };
}

/** The runs that consume the most: the per-process summary without the calls of no process. */
export function runsOf(rows: UsageSummaryRow[] | undefined): { runs: RunSlice[]; avg: number } {
  const list = slicesOf('process', rows).filter((s) => s.key && s.total > 0);
  const avg = list.length ? list.reduce((a, s) => a + s.total, 0) / list.length : 0;
  return { runs: list.map((s) => ({ ...s, ratio: avg ? s.total / avg : 0 })), avg };
}

/** The most expensive calls of a list, biggest first. */
export function topCalls(calls: LLMCall[], n = 10): LLMCall[] {
  const total = (c: LLMCall) => int(c.inputTokens) + int(c.outputTokens);
  return [...calls].sort((a, b) => total(b) - total(a)).slice(0, n);
}

/** The periods of the usage views, `[id, label]`. `today` starts at local midnight: the person's working day. */
export const USAGE_PERIODS: [string, string][] = [
  ['today', 'Today'],
  ['24h', 'Last 24 hours'],
  ['7d', 'Last 7 days'],
  ['30d', 'Last 30 days'],
  ['all', 'All history'],
];

const PERIOD_MS: Record<string, number> = { '24h': DAY, '7d': 7 * DAY, '30d': 30 * DAY };

/** Start (ms) of a period ending at `now`: local midnight for `today`, 0 (no bound) for all history or an unknown id. */
export function periodStart(period: string, now: number): number {
  if (period === 'today') {
    const d = new Date(now);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  }
  const ms = PERIOD_MS[period];
  return ms ? now - ms : 0;
}

/** A token count in a few characters: 950, 12.3k, 4.1M. */
export function compactTokens(v: number): string {
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}k`;
  return String(Math.round(v));
}
