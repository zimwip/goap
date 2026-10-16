// Risks and actions of a change (ADR 0036 §1): items of type risk / action, each a version of the record of its key;
// the register is the last version of each key among the items in effect (the server's domain.Risks / ActionItems).
import type { ChangeItem } from './api';

export interface RiskRecord {
  key: string;
  title: string;
  description: string;
  probability: number;
  impact: number;
  score: number;
  status: string;
  owner: string;
  actions: string[];
  versions: number;
  by: string;
}

export interface ActionRecord {
  key: string;
  title: string;
  status: string;
  owner: string;
  due: string;
  for: string;
  result: string;
  /** mitigation (default) or poc: a proof of concept or a prototype built in a sub-change (ADR 0131) */
  kind: string;
  /** of a poc: derisk (the risks it checks) or explore (a prototype of the need, its hypotheses) */
  purpose: string;
  risks: string[];
  hypotheses: string[];
  /** the sub-change carrying a poc, once opened */
  change: string;
  versions: number;
  by: string;
}

export const RISK_STATUSES = ['open', 'mitigating', 'accepted', 'occurred', 'closed'] as const;
export const ACTION_STATUSES = ['open', 'done', 'cancelled'] as const;
export const KIND_POC = 'poc';
export const PURPOSE_DERISK = 'derisk';
export const PURPOSE_EXPLORE = 'explore';
/** from this score an open risk needs a mitigation action */
export const HIGH_RISK = 9;

const inEffect = (it: ChangeItem, superseded: Set<string>) => it.status !== 'rejected' && it.status !== 'superseded' && !superseded.has(it.id ?? '');

function fold(items: ChangeItem[], kind: string): { key: string; data: Record<string, unknown>; versions: number; by: string }[] {
  const superseded = new Set(items.flatMap((it) => it.supersedes ?? []));
  const order: string[] = [];
  const last = new Map<string, { data: Record<string, unknown>; versions: number; by: string }>();
  for (const it of items) {
    if (it.type !== kind || !inEffect(it, superseded)) continue;
    const key = String((it.data as Record<string, unknown> | undefined)?.key ?? '').trim();
    if (!key) continue;
    const prev = last.get(key);
    if (!prev) order.push(key);
    last.set(key, { data: { ...(prev?.data ?? {}), ...(it.data as Record<string, unknown>) }, versions: (prev?.versions ?? 0) + 1, by: it.producedBy ?? '' });
  }
  return order.map((key) => ({ key, ...last.get(key)! }));
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');
const strs = (v: unknown) => (Array.isArray(v) ? v.map(String).filter((x) => x) : []);
const num = (v: unknown) => (typeof v === 'number' ? v : Number(v) || 0);

export function riskRegister(items: ChangeItem[]): RiskRecord[] {
  return fold(items, 'risk').map(({ key, data, versions, by }) => {
    const probability = num(data.probability);
    const impact = num(data.impact);
    return {
      key,
      title: str(data.title),
      description: str(data.description),
      probability,
      impact,
      score: probability * impact,
      status: str(data.status) || 'open',
      owner: str(data.owner),
      actions: Array.isArray(data.actions) ? data.actions.map(String) : [],
      versions,
      by,
    };
  });
}

export function actionList(items: ChangeItem[]): ActionRecord[] {
  return fold(items, 'action').map(({ key, data, versions, by }) => {
    const kind = str(data.kind) || 'mitigation';
    return {
      key,
      title: str(data.title),
      status: str(data.status) || 'open',
      owner: str(data.owner),
      due: str(data.due),
      for: str(data.for),
      result: str(data.result),
      kind,
      purpose: str(data.purpose) || (kind === KIND_POC ? PURPOSE_DERISK : ''),
      risks: strs(data.risks),
      hypotheses: strs(data.hypotheses),
      change: str(data.change),
      versions,
      by,
    };
  });
}

/** Whether an action covers a risk (the server's ActionItem.Covers, ADR 0131): it answers it and is not cancelled; a
 *  concluded proof of concept covers nothing any more, its verdict settled the risk or asks for another mitigation. */
export function covers(a: ActionRecord, riskKey: string): boolean {
  if (a.for !== riskKey && !a.risks.includes(riskKey)) return false;
  return a.status !== 'cancelled' && !(a.kind === KIND_POC && a.status === 'done');
}

/** The data of a new proof of concept (purpose derisk, the risks it checks) or prototype (explore, its statements). */
export function pocData(key: string, title: string, purpose: string, risks: string[], hypotheses: string[], owner = ''): Record<string, string | string[]> {
  const d: Record<string, string | string[]> = { key, title, kind: KIND_POC, purpose, owner, status: 'open' };
  if (risks.length) {
    d.for = risks[0];
    d.risks = risks;
  }
  if (hypotheses.length) d.hypotheses = hypotheses;
  return d;
}

export const liveRisk = (r: RiskRecord) => r.status === 'open' || r.status === 'mitigating';

/** A live high risk with no action answering it. */
export function unmitigated(r: RiskRecord, actions: ActionRecord[]): boolean {
  return liveRisk(r) && r.score >= HIGH_RISK && !actions.some((a) => covers(a, r.key));
}

/** Next free key of a kind ("RSK-3"). */
export function nextKey(prefix: string, keys: string[]): string {
  let n = 0;
  for (const k of keys) {
    const m = new RegExp(`^${prefix}(\\d+)$`).exec(k);
    if (m) n = Math.max(n, Number(m[1]));
  }
  return `${prefix}${n + 1}`;
}
