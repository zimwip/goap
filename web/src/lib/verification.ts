// Verification and derogations of a change (ADR 0075): items of kind verification (a state per effect of an action run,
// folded like the server's verify.Subjects) and derogation (a waiver with a rule, a signatory and an expiry, each a
// version of the record of its key like a risk).
import type { ChangeItem, Struct } from './api';

export type VerificationState = 'produced' | 'verified' | 'accepted' | 'accepted_with_reserve' | 'rejected';

export interface VerificationRecord {
  execution: string;
  action: string;
  impact: string;
  oracle: string;
  independent: boolean;
  state: VerificationState | string;
  producer: string;
  by: string;
  derogation: string;
  /** still waits for a verdict */
  open: boolean;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');
const list = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);
const inEffect = (it: ChangeItem, superseded: Set<string>) => it.status !== 'rejected' && it.status !== 'superseded' && !superseded.has(it.id ?? '');

/** The state of each effect of the action runs, from the entries in the order they were written. */
export function verifications(items: ChangeItem[]): VerificationRecord[] {
  const superseded = new Set(items.flatMap((it) => it.supersedes ?? []));
  const out: VerificationRecord[] = [];
  const byImpact = new Map<string, number>();
  for (const it of items) {
    if (it.kind !== 'verification' || !inEffect(it, superseded)) continue;
    const d = (it.data ?? {}) as Record<string, unknown>;
    const state = str(d.state);
    if (state === 'produced') {
      const base: VerificationRecord = {
        execution: it.execution ?? '',
        action: str(d.action),
        impact: '',
        oracle: str(d.oracle),
        independent: d.independent === true,
        state,
        producer: it.producedBy ?? '',
        by: '',
        derogation: '',
        open: true,
      };
      const impacts = list(d.impacts);
      if (!impacts.length) out.push(base);
      for (const id of impacts) {
        const rec = { ...base, impact: id };
        const at = byImpact.get(id);
        if (at !== undefined) out[at] = rec; // produced again: starts over
        else {
          byImpact.set(id, out.length);
          out.push(rec);
        }
      }
      continue;
    }
    const at = byImpact.get(str(d.impact));
    if (at === undefined) continue;
    const rec = out[at];
    rec.state = state;
    rec.open = state === 'verified';
    rec.derogation = state === 'accepted_with_reserve' ? str(d.derogation) : '';
    if (str(d.by)) rec.by = str(d.by);
  }
  return out;
}

export interface DerogationRecord {
  key: string;
  rule: string;
  target: string;
  reason: string;
  signatory: string;
  /** RFC 3339 or a date */
  expires: string;
  status: 'open' | 'closed' | string;
  versions: number;
}

export const DEROGATION_STATUSES = ['open', 'closed'] as const;

/** The derogations of the change: the current version of each key, in the order they were signed. */
export function derogationRegister(items: ChangeItem[]): DerogationRecord[] {
  const superseded = new Set(items.flatMap((it) => it.supersedes ?? []));
  const order: string[] = [];
  const last = new Map<string, { d: Record<string, unknown>; versions: number }>();
  for (const it of items) {
    if (it.kind !== 'derogation' || !inEffect(it, superseded)) continue;
    const d = (it.data ?? {}) as Record<string, unknown>;
    const key = str(d.key).trim();
    if (!key) continue;
    const prev = last.get(key);
    if (!prev) order.push(key);
    last.set(key, { d, versions: (prev?.versions ?? 0) + 1 });
  }
  return order.map((key) => {
    const { d, versions } = last.get(key)!;
    return { key, rule: str(d.rule), target: str(d.target), reason: str(d.reason), signatory: str(d.signatory), expires: str(d.expires), status: str(d.status) || 'open', versions };
  });
}

/** The instant a derogation runs out: RFC 3339, or the end of the day (UTC) for a date. NaN when unreadable. */
export function expiryOf(d: Pick<DerogationRecord, 'expires'>): number {
  if (/^\d{4}-\d{2}-\d{2}$/.test(d.expires)) return Date.parse(`${d.expires}T23:59:59.999Z`);
  return Date.parse(d.expires);
}

export const openDerogation = (d: DerogationRecord) => d.status !== 'closed';
export const expiredDerogation = (d: DerogationRecord, now = Date.now()) => openDerogation(d) && !(expiryOf(d) > now);

/** The version that closes a derogation: it restates what the previous one said (the server requires it), signed by whoever closes it. */
export function closing(d: DerogationRecord, by: string): Struct {
  return { key: d.key, rule: d.rule, target: d.target, reason: d.reason, signatory: by, expires: d.expires, status: 'closed' };
}
