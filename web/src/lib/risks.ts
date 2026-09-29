// Risks and actions of a change (ADR 0036 §1): items of kind risk / action, each a version of the record of its key;
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
  versions: number;
  by: string;
}

export const RISK_STATUSES = ['open', 'mitigating', 'accepted', 'occurred', 'closed'] as const;
export const ACTION_STATUSES = ['open', 'done', 'cancelled'] as const;
/** from this score an open risk needs a mitigation action */
export const HIGH_RISK = 9;

const inEffect = (it: ChangeItem, superseded: Set<string>) => it.status !== 'rejected' && it.status !== 'superseded' && !superseded.has(it.id ?? '');

function fold(items: ChangeItem[], kind: string): { key: string; data: Record<string, unknown>; versions: number; by: string }[] {
  const superseded = new Set(items.flatMap((it) => it.supersedes ?? []));
  const order: string[] = [];
  const last = new Map<string, { data: Record<string, unknown>; versions: number; by: string }>();
  for (const it of items) {
    if (it.kind !== kind || !inEffect(it, superseded)) continue;
    const key = String((it.data as Record<string, unknown> | undefined)?.key ?? '').trim();
    if (!key) continue;
    const prev = last.get(key);
    if (!prev) order.push(key);
    last.set(key, { data: { ...(prev?.data ?? {}), ...(it.data as Record<string, unknown>) }, versions: (prev?.versions ?? 0) + 1, by: it.producedBy ?? '' });
  }
  return order.map((key) => ({ key, ...last.get(key)! }));
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');
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
  return fold(items, 'action').map(({ key, data, versions, by }) => ({
    key,
    title: str(data.title),
    status: str(data.status) || 'open',
    owner: str(data.owner),
    due: str(data.due),
    for: str(data.for),
    result: str(data.result),
    versions,
    by,
  }));
}

export const liveRisk = (r: RiskRecord) => r.status === 'open' || r.status === 'mitigating';

/** A live high risk with no action answering it. */
export function unmitigated(r: RiskRecord, actions: ActionRecord[]): boolean {
  return liveRisk(r) && r.score >= HIGH_RISK && !actions.some((a) => a.for === r.key && a.status !== 'cancelled');
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
