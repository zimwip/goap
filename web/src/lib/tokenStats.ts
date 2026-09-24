// Token consumption statistics computed from the processes (steps and LLM calls).
import type { Int64, Process } from './api';

const num = (v: Int64 | undefined | null) => Number(v ?? 0) || 0;

/** One LLM call (or the usage of a step that did not detail its calls). */
export interface TokenEvent {
  time: number;
  processId: string;
  action: string;
  step: number;
  model: string;
  input: number;
  output: number;
  error: boolean;
}

export interface Bucket {
  key: string;
  label: string;
  input: number;
  output: number;
}

export interface Slice {
  key: string;
  input: number;
  output: number;
  calls: number;
  total: number;
}

export interface RunStat {
  id: string;
  title: string;
  agent: string;
  methodology: string;
  status: string;
  createdAt: string;
  input: number;
  output: number;
  total: number;
  /** tokens of the run itself, without its sub-agents */
  own: number;
  calls: number;
  subAgents: number;
  /** total relative to the average run */
  ratio: number;
}

export interface TokenStats {
  input: number;
  output: number;
  total: number;
  calls: number;
  errors: number;
  runs: number;
  avgPerRun: number;
  medianPerRun: number;
  buckets: Bucket[];
  byModel: Slice[];
  byAgent: Slice[];
  byAction: Slice[];
  topRuns: RunStat[];
  topCalls: (TokenEvent & { title: string; total: number })[];
}

export function eventsOf(processes: Process[]): TokenEvent[] {
  const out: TokenEvent[] = [];
  for (const p of processes) {
    const id = p.id ?? '';
    const created = Date.parse(p.createdAt ?? '') || 0;
    const steps = p.steps ?? [];
    let detailed = 0;
    let stepIn = 0;
    let stepOut = 0;
    for (const s of steps) {
      const t = Date.parse(s.startedAt ?? '') || created;
      const calls = s.llmCalls ?? [];
      for (const c of calls) {
        out.push({ time: t, processId: id, action: s.action ?? '', step: s.index ?? 0, model: c.model || 'unknown', input: num(c.inputTokens), output: num(c.outputTokens), error: !!(c as { error?: string }).error });
        detailed++;
        stepIn += num(c.inputTokens);
        stepOut += num(c.outputTokens);
      }
      if (!calls.length && (num(s.usage?.inputTokens) || num(s.usage?.outputTokens))) {
        out.push({ time: t, processId: id, action: s.action ?? '', step: s.index ?? 0, model: 'unknown', input: num(s.usage?.inputTokens), output: num(s.usage?.outputTokens), error: false });
        detailed++;
        stepIn += num(s.usage?.inputTokens);
        stepOut += num(s.usage?.outputTokens);
      }
    }
    // intent / ranking calls are not in a step: what the totals hold beyond the steps
    const restIn = num(p.usage?.inputTokens) - stepIn;
    const restOut = num(p.usage?.outputTokens) - stepOut;
    if (restIn > 0 || restOut > 0) {
      out.push({ time: created, processId: id, action: detailed ? '(intent)' : '(run)', step: -1, model: 'unknown', input: Math.max(restIn, 0), output: Math.max(restOut, 0), error: false });
    }
  }
  return out;
}

function slices(events: TokenEvent[], key: (e: TokenEvent) => string): Slice[] {
  const m = new Map<string, Slice>();
  for (const e of events) {
    const k = key(e);
    const s = m.get(k) ?? { key: k, input: 0, output: 0, calls: 0, total: 0 };
    s.input += e.input;
    s.output += e.output;
    s.calls += 1;
    s.total += e.input + e.output;
    m.set(k, s);
  }
  return [...m.values()].sort((a, b) => b.total - a.total);
}

function bucketsOf(events: TokenEvent[], from: number, to: number, hourly: boolean): Bucket[] {
  const step = hourly ? 3_600_000 : 86_400_000;
  const start = new Date(from);
  if (hourly) start.setMinutes(0, 0, 0);
  else start.setHours(0, 0, 0, 0);
  const first = start.getTime();
  const count = Math.min(400, Math.max(1, Math.floor((to - first) / step) + 1));
  const out: Bucket[] = [];
  for (let i = 0; i < count; i++) {
    const d = new Date(first + i * step);
    out.push({
      key: String(first + i * step),
      label: hourly ? d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }),
      input: 0,
      output: 0,
    });
  }
  for (const e of events) {
    const i = Math.floor((e.time - first) / step);
    if (i >= 0 && i < out.length) {
      out[i].input += e.input;
      out[i].output += e.output;
    }
  }
  return out;
}

/** `sinceMs`: 0 for the whole history. */
export function computeStats(processes: Process[], sinceMs: number, now = Date.now()): TokenStats {
  const byId = new Map(processes.map((p) => [p.id ?? '', p]));
  const rootOf = (id: string): string => {
    let cur = id;
    for (let i = 0; i < 50; i++) {
      const parent = byId.get(cur)?.parentId;
      if (!parent || !byId.has(parent)) return cur;
      cur = parent;
    }
    return cur;
  };
  const events = eventsOf(processes).filter((e) => !sinceMs || e.time >= sinceMs);

  const runs = new Map<string, RunStat>();
  const subs = new Map<string, Set<string>>();
  for (const e of events) {
    const root = rootOf(e.processId);
    const rp = byId.get(root);
    const r = runs.get(root) ?? {
      id: root,
      title: rp?.title || rp?.goal || rp?.agent || root.slice(0, 8),
      agent: rp?.agent ?? '',
      methodology: rp?.methodology ?? '',
      status: String(rp?.status ?? ''),
      createdAt: rp?.createdAt ?? '',
      input: 0,
      output: 0,
      total: 0,
      own: 0,
      calls: 0,
      subAgents: 0,
      ratio: 0,
    };
    r.input += e.input;
    r.output += e.output;
    r.total += e.input + e.output;
    r.calls += 1;
    if (e.processId === root) r.own += e.input + e.output;
    else subs.set(root, (subs.get(root) ?? new Set()).add(e.processId));
    runs.set(root, r);
  }
  const list = [...runs.values()].filter((r) => r.total > 0);
  for (const r of list) r.subAgents = subs.get(r.id)?.size ?? 0;
  const total = list.reduce((a, r) => a + r.total, 0);
  const avg = list.length ? total / list.length : 0;
  const sorted = list.map((r) => r.total).sort((a, b) => a - b);
  const median = sorted.length ? sorted[Math.floor(sorted.length / 2)] : 0;
  for (const r of list) r.ratio = avg ? r.total / avg : 0;
  list.sort((a, b) => b.total - a.total);

  const input = events.reduce((a, e) => a + e.input, 0);
  const output = events.reduce((a, e) => a + e.output, 0);
  const from = sinceMs || (events.length ? Math.min(...events.map((e) => e.time)) : now);
  const hourly = !!sinceMs && now - sinceMs <= 2 * 86_400_000;
  const titleOf = (id: string) => runs.get(rootOf(id))?.title ?? id.slice(0, 8);
  return {
    input,
    output,
    total: input + output,
    calls: events.length,
    errors: events.filter((e) => e.error).length,
    runs: list.length,
    avgPerRun: avg,
    medianPerRun: median,
    buckets: bucketsOf(events, from, now, hourly),
    byModel: slices(events, (e) => e.model),
    byAgent: slices(events, (e) => byId.get(e.processId)?.agent || byId.get(e.processId)?.methodology || 'unknown'),
    byAction: slices(events, (e) => e.action || 'unknown'),
    topRuns: list,
    topCalls: events
      .map((e) => ({ ...e, title: titleOf(e.processId), total: e.input + e.output }))
      .sort((a, b) => b.total - a.total)
      .slice(0, 10),
  };
}
