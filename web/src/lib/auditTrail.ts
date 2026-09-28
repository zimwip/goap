// The audit trail of a change: one chronological list of everything that happened to it, merged from its three
// append-only sources: the execution journal (processes, plans, actions, human approvals: ADR 0011), the impact log
// (every operation on its change impacts: ADR 0029) and the facts of its blackboard (items, flow events: ADR 0017).
// Each entry says when, who, on which flow and through which action run.
import { shortId, type Change, type ChangeItem, type ExecutionRecord, type ImpactEvent, type JsonValue, type NodeRef } from './api';

export type AuditSource = 'change' | 'process' | 'plan' | 'action' | 'approval' | 'impact' | 'fact' | 'flow';

export const AUDIT_SOURCES: { id: AuditSource; label: string }[] = [
  { id: 'process', label: 'Processes' },
  { id: 'action', label: 'Actions' },
  { id: 'approval', label: 'Approvals' },
  { id: 'impact', label: 'Impacts' },
  { id: 'fact', label: 'Facts' },
  { id: 'flow', label: 'Flows' },
  { id: 'plan', label: 'Plans' },
  { id: 'change', label: 'Change' },
];

export interface AuditEntry {
  key: string;
  at: string;
  source: AuditSource;
  /** what happened: action, written, decision, flow open… */
  label: string;
  /** what it happened to: action name, change impact key, fact type */
  subject: string;
  summary: string;
  /** flow branch ('' = the main flow) */
  flow: string;
  /** the principal, the agent or the component */
  by: string;
  /** the journal record of the action run behind it (an action's own record for an action) */
  execution: string;
  processId: string;
  tone: 'ok' | 'error' | 'warn' | 'neutral';
  record?: ExecutionRecord;
  event?: ImpactEvent;
  item?: ChangeItem;
}

const v = (r: NodeRef | undefined) => (r ? `v${r.version ?? 0}` : '');
const str = (x: JsonValue | undefined) => (typeof x === 'string' ? x : '');

/** "agent · step N · action" of an action run. */
export function runLabel(r: ExecutionRecord | undefined): string {
  if (!r) return '';
  return [r.agent, r.step !== undefined ? `step ${r.step + 1}` : '', r.specialization || r.action].filter(Boolean).join(' · ');
}

function fromRecord(r: ExecutionRecord): AuditEntry {
  const base = {
    key: `r:${r.id ?? `${r.processId}:${r.seq}`}`,
    at: r.startedAt ?? '',
    flow: '',
    by: r.actor || r.agent || '',
    execution: r.id ?? '',
    processId: r.processId ?? '',
    record: r,
  };
  switch (r.kind) {
    case 'action': {
      const waiting = str(r.data?.['waiting']);
      const status = r.error ? 'error' : waiting ? `waiting (${waiting})` : r.effectsMet === true ? 'effects met' : r.effectsMet === false ? 'effects not met' : 'running';
      return {
        ...base,
        source: 'action',
        label: 'action',
        subject: r.specialization || r.action || '',
        summary: [runLabel(r), status, r.actionKind].filter(Boolean).join(' · '),
        tone: r.error || r.effectsMet === false ? 'error' : waiting ? 'warn' : r.effectsMet ? 'ok' : 'neutral',
      };
    }
    case 'approval': {
      const approved = r.data?.['approved'] !== false;
      return {
        ...base,
        source: 'approval',
        label: approved ? 'approved' : 'rejected',
        subject: r.action ?? '',
        summary: str(r.data?.['comment']),
        tone: approved ? 'ok' : 'warn',
      };
    }
    case 'tick':
      return { ...base, source: 'plan', label: 'plan', subject: r.plan?.[0] ?? '', summary: r.plan?.length ? r.plan.join(' → ') : 'no plan', tone: r.error ? 'error' : 'neutral' };
    case 'process.started':
      return { ...base, source: 'process', label: 'started', subject: r.agent ?? '', summary: [str(r.data?.['title']), r.goal ? `goal ${r.goal}` : '', str(r.data?.['intent'])].filter(Boolean).join(' · '), tone: 'neutral' };
    case 'process.ended':
      return { ...base, source: 'process', label: 'ended', subject: r.agent ?? '', summary: r.status ?? '', tone: r.status === 'completed' ? 'ok' : r.status === 'failed' || r.status === 'stuck' ? 'error' : 'neutral' };
  }
  return { ...base, source: 'process', label: r.kind ?? '', subject: r.action ?? '', summary: '', tone: 'neutral' };
}

function fromEvent(e: ImpactEvent, keys: Map<string, string>): AuditEntry {
  let summary = '';
  switch (e.op) {
    case 'declared':
      summary = `${e.state?.intent ?? ''}${e.state?.pre ? ` from ${v(e.state.pre)}` : ''}${e.state?.rationale ? ` — ${e.state.rationale}` : ''}`;
      break;
    case 'written':
      summary = `version ${v(e.post)}`;
      break;
    case 'reviewed':
    case 'discarded':
      summary = `${e.review?.status ?? ''}${e.review?.comment ? ` — ${e.review.comment}` : ''}`;
      break;
    case 'adopted':
      summary = `flow ${shortId(e.flow)} adopted${e.stale?.length ? `, replaces ${e.stale.length} run${e.stale.length > 1 ? 's' : ''}` : ''}`;
      break;
    case 'landed':
      summary = `landed as ${v(e.landed)}`;
      break;
    case 'rebased':
      summary = `pre moved to ${v(e.pre)}, to re-check`;
      break;
  }
  return {
    key: `e:${e.id}`,
    at: e.at ?? '',
    source: 'impact',
    label: e.op ?? '',
    subject: e.impactId ? (keys.get(e.impactId) ?? shortId(e.impactId)) : '',
    summary,
    flow: e.flow ?? '',
    by: e.by ?? '',
    execution: e.execution ?? '',
    processId: '',
    tone: e.op === 'discarded' || e.op === 'rebased' || e.review?.status === 'rejected' ? 'warn' : e.op === 'landed' ? 'ok' : 'neutral',
    event: e,
  };
}

function fromItem(it: ChangeItem): AuditEntry {
  const base = { key: `i:${it.id}`, at: it.createdAt ?? '', flow: it.flow ?? '', by: it.producedBy ?? '', execution: it.execution ?? '', processId: '', item: it };
  if (it.kind === 'flow' && it.flowEvent) {
    const f = it.flowEvent;
    return {
      ...base,
      source: 'flow',
      label: `flow ${f.op ?? ''}`,
      subject: shortId(f.flow),
      summary: [f.fromStep !== undefined && f.op === 'open' ? `from step ${f.fromStep + 1}` : '', f.reason, f.stale?.length ? `${f.stale.length} stale item(s)` : ''].filter(Boolean).join(' · '),
      flow: f.flow ?? base.flow,
      by: f.by || base.by,
      tone: f.op === 'discard' ? 'warn' : f.op === 'adopt' ? 'ok' : 'neutral',
    };
  }
  if (it.kind === 'decision' && it.decision) {
    return {
      ...base,
      source: 'fact',
      label: it.decision.accept ? 'accepted' : 'rejected',
      subject: `decision on ${shortId(it.decision.item)}`,
      summary: it.decision.comment ?? '',
      tone: it.decision.accept ? 'ok' : 'warn',
    };
  }
  const title = str(it.data?.['title']) || str(it.data?.['summary']) || str(it.data?.['name']);
  return { ...base, source: 'fact', label: it.kind ?? 'fact', subject: it.type ?? '', summary: [title, it.status].filter(Boolean).join(' · '), tone: 'neutral' };
}

/** The audit trail of a change, oldest first. */
export function buildTrail(change: Change, events: ImpactEvent[], records: ExecutionRecord[]): AuditEntry[] {
  const keys = new Map<string, string>();
  for (const cn of change.nodes ?? []) keys.set(cn.id ?? '', cn.key ?? '');
  for (const e of events) if (e.state?.key) keys.set(e.impactId ?? '', e.state.key);
  const out: AuditEntry[] = [];
  if (change.createdAt) {
    out.push({
      key: 'change',
      at: change.createdAt,
      source: 'change',
      label: 'created',
      subject: change.title ?? '',
      summary: [change.intent, change.methodology ? `methodology ${change.methodology}` : '', change.namespace ? `namespace ${change.namespace}` : '', change.ownerOrg ? `held by ${change.ownerOrg}` : ''].filter(Boolean).join(' · '),
      flow: '',
      by: '',
      execution: '',
      processId: '',
      tone: 'neutral',
    });
  }
  out.push(...records.map(fromRecord), ...events.map((e) => fromEvent(e, keys)), ...(change.items ?? []).map(fromItem));
  const order: Record<AuditSource, number> = { change: 0, process: 1, plan: 2, action: 3, approval: 4, flow: 5, fact: 6, impact: 7 };
  return out
    .map((e, i) => ({ e, i }))
    .sort((a, b) => a.e.at.localeCompare(b.e.at) || order[a.e.source] - order[b.e.source] || a.i - b.i)
    .map(({ e }) => e);
}

/** The trail as CSV (one line per entry). */
export function trailCSV(entries: AuditEntry[], runs: Map<string, ExecutionRecord>): string {
  const cell = (s: string) => (/[",\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s);
  const head = ['at', 'source', 'what', 'subject', 'summary', 'flow', 'by', 'action run', 'execution'];
  const lines = entries.map((e) =>
    [e.at, e.source, e.label, e.subject, e.summary, e.flow || 'main', e.by, runLabel(runs.get(e.execution)), e.execution].map(cell).join(','),
  );
  return [head.join(','), ...lines].join('\n');
}
