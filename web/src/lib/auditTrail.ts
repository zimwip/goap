// The audit trail of a change: one chronological list of everything that happened to it, merged from its three
// append-only sources: the execution journal (processes, scheduling, plans, actions, human approvals: ADR 0011), the
// impact log (every operation on its change impacts: ADR 0029) and the facts of its blackboard (items, flow events:
// ADR 0017). Each entry says when, who, on which flow and through which action run. A change is a flow of events:
// flow branches fork from it (a relaunch, alternatives to compare), and are merged back (adopted) or dropped
// (discarded); the entries carry these marks so the trail can be drawn as branches.
import { int, shortId, type Change, type ChangeItem, type ExecutionRecord, type ImpactEvent, type JsonValue, type LogEntry, type NodeRef } from './api';

export type AuditSource = 'change' | 'process' | 'schedule' | 'plan' | 'action' | 'approval' | 'impact' | 'fact' | 'flow';

export const AUDIT_SOURCES: { id: AuditSource; label: string }[] = [
  { id: 'process', label: 'Processes' },
  { id: 'schedule', label: 'Scheduling' },
  { id: 'action', label: 'Actions' },
  { id: 'approval', label: 'Approvals' },
  { id: 'impact', label: 'Impacts' },
  { id: 'fact', label: 'Facts' },
  { id: 'flow', label: 'Flows' },
  { id: 'plan', label: 'Plans' },
  { id: 'change', label: 'Change' },
];

/** The types of the log each source reads (the server filters on them). */
export const SOURCE_TYPES: Record<Exclude<AuditSource, 'change'>, string[]> = {
  process: ['journal.process.started', 'journal.process.ended'],
  schedule: ['journal.schedule'],
  plan: ['journal.tick'],
  action: ['journal.action'],
  approval: ['journal.approval'],
  impact: ['impact.'],
  fact: ['fact.artifact', 'fact.decision', 'fact.merge'],
  flow: ['fact.flow'],
};

/** The source of a log type. */
export function sourceOfType(type: string): AuditSource | undefined {
  for (const [s, types] of Object.entries(SOURCE_TYPES)) {
    if (types.some((t) => t === type || (t.endsWith('.') && type.startsWith(t)))) return s as AuditSource;
  }
  return undefined;
}

export interface AuditEntry {
  key: string;
  /** position in the log of the change (0: the change itself) */
  seq: number;
  at: string;
  source: AuditSource;
  /** what happened: action, written, decision, flow open… */
  label: string;
  /** what it happened to: action name, change impact key, fact type */
  subject: string;
  summary: string;
  /** flow branch the entry happened on ('' = the main flow) */
  flow: string;
  /** a flow branch forks here (opened), is merged back here (adopted) or ends here (discarded) */
  fork?: string;
  merge?: string;
  end?: boolean;
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

/** An entry before it gets its position in the log. */
type Entry = Omit<AuditEntry, 'seq'>;

/** The rest of a plan after its next action, the first steps only (the full plan is in the record). */
function planSummary(plan: string[] | undefined): string {
  if (!plan?.length) return 'no plan';
  const rest = plan.slice(1);
  if (!rest.length) return 'last step of the plan';
  return `then ${rest.slice(0, 3).join(' → ')}${rest.length > 3 ? ` (+${rest.length - 3})` : ''}`;
}

function fromRecord(r: ExecutionRecord): Entry {
  const base = {
    key: `r:${r.id ?? `${r.processId}:${r.seq}`}`,
    at: r.startedAt ?? '',
    by: r.actor || r.agent || '',
    flow: r.flow ?? '',
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
    case 'tick': {
      const done = r.data?.['goalSatisfied'] === true;
      return {
        ...base,
        source: 'plan',
        label: done ? 'goal reached' : 'plan',
        subject: done ? r.goal ?? '' : `next: ${r.plan?.[0] ?? '—'}`,
        summary: done ? (r.data?.['awaitingDecision'] ? 'waiting for the decision on the flow' : '') : r.error || planSummary(r.plan),
        tone: r.error ? 'error' : done ? 'ok' : 'neutral',
      };
    }
    case 'schedule': {
      const reason = str(r.data?.['reason']);
      const waited = r.startedAt && r.endedAt ? Date.parse(r.endedAt) - Date.parse(r.startedAt) : 0;
      const cause = [
        str(r.data?.['action']) && `after ${str(r.data?.['action'])}`,
        str(r.data?.['parent']) && `for process ${shortId(str(r.data?.['parent']))}${str(r.data?.['call']) ? ` (${str(r.data?.['call'])})` : ''}`,
        str(r.data?.['child']) && `sub-agent ${shortId(str(r.data?.['child']))} ended`,
        str(r.data?.['process']) && `relaunch of ${shortId(str(r.data?.['process']))}${typeof r.data?.['fromStep'] === 'number' ? ` from step ${(r.data['fromStep'] as number) + 1}` : ''}`,
        str(r.data?.['trigger']) && `trigger ${str(r.data?.['trigger'])}`,
        str(r.data?.['why']),
        waited > 0 ? `waited ${waited} ms` : '',
      ].filter(Boolean);
      return { ...base, source: 'schedule', label: `scheduled: ${reason}`, subject: r.agent ?? '', summary: cause.join(' · '), tone: 'neutral' };
    }
    case 'process.started':
      return { ...base, source: 'process', label: 'started', subject: r.agent ?? '', summary: [str(r.data?.['title']), r.goal ? `goal ${r.goal}` : '', str(r.data?.['intent'])].filter(Boolean).join(' · '), tone: 'neutral' };
    case 'process.ended':
      return { ...base, source: 'process', label: 'ended', subject: r.agent ?? '', summary: r.status ?? '', tone: r.status === 'completed' ? 'ok' : r.status === 'failed' || r.status === 'stuck' ? 'error' : 'neutral' };
  }
  return { ...base, source: 'process', label: r.kind ?? '', subject: r.action ?? '', summary: '', tone: 'neutral' };
}

function fromEvent(e: ImpactEvent, keys: Map<string, string>, parents: Map<string, string>): Entry {
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
    // the adoption of a flow happens on the flow it merges into
    flow: e.op === 'adopted' ? (parents.get(e.flow ?? '') ?? '') : (e.flow ?? ''),
    by: e.by ?? '',
    execution: e.execution ?? '',
    processId: '',
    tone: e.op === 'discarded' || e.op === 'rebased' || e.review?.status === 'rejected' ? 'warn' : e.op === 'landed' ? 'ok' : 'neutral',
    event: e,
  };
}

function fromItem(it: ChangeItem, parents: Map<string, string>): Entry {
  const base = { key: `i:${it.id}`, at: it.createdAt ?? '', flow: it.flow ?? '', by: it.producedBy ?? '', execution: it.execution ?? '', processId: '', item: it };
  if (it.kind === 'flow' && it.flowEvent) {
    const f = it.flowEvent;
    const id = f.flow ?? '';
    const parent = parents.get(id) ?? '';
    const labels: Record<string, string> = { open: 'flow opened', adopt: 'flow adopted', discard: 'flow discarded' };
    return {
      ...base,
      source: 'flow',
      label: labels[f.op ?? ''] ?? `flow ${f.op ?? ''}`,
      subject: shortId(id),
      summary: [
        f.op === 'open' ? `from ${parent ? `flow ${shortId(parent)}` : 'the main flow'}${f.fromStep !== undefined ? `, step ${f.fromStep + 1}` : ''}` : '',
        f.op === 'adopt' ? `merged into ${parent ? `flow ${shortId(parent)}` : 'the main flow'}` : '',
        f.reason,
        f.stale?.length ? `${f.stale.length} stale item(s)` : '',
      ]
        .filter(Boolean)
        .join(' · '),
      // opened and adopted on the flow it forks from / merges into; discarded on its own
      flow: f.op === 'discard' ? id : parent,
      fork: f.op === 'open' ? id : undefined,
      merge: f.op === 'adopt' ? id : undefined,
      end: f.op === 'discard',
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

/** The flow each flow branch forks from, from the flow events of the log. */
export function flowParents(entries: LogEntry[]): Map<string, string> {
  const parents = new Map<string, string>();
  for (const l of entries) {
    if (l.type !== 'fact.flow') continue;
    const it = JSON.parse(l.payload ?? '{}') as ChangeItem;
    if (it.flowEvent?.op === 'open') parents.set(it.flowEvent.flow ?? '', it.flowEvent.parent ?? '');
  }
  return parents;
}

/** The audit trail of a change from its log (ADR 0030), in the order of the log. */
export function buildTrail(change: Change, log: LogEntry[], parents: Map<string, string>, withChange = true): AuditEntry[] {
  const keys = new Map<string, string>();
  for (const cn of change.nodes ?? []) keys.set(cn.id ?? '', cn.key ?? '');
  const out: AuditEntry[] = [];
  if (withChange && change.createdAt) {
    out.push({
      key: 'change',
      seq: 0,
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
  for (const l of log) {
    const payload = JSON.parse(l.payload ?? '{}');
    let e: Entry;
    if (l.type?.startsWith('journal.')) e = fromRecord(payload as ExecutionRecord);
    else if (l.type?.startsWith('impact.')) {
      const ev = payload as ImpactEvent;
      if (ev.state?.key) keys.set(ev.impactId ?? '', ev.state.key);
      e = fromEvent(ev, keys, parents);
    } else e = fromItem(payload as ChangeItem, parents);
    out.push({ ...e, seq: int(l.seq) });
  }
  return out;
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

/** Where each entry of a trail (in display order) is drawn: one lane per flow branch, main on the left, a lane reused
 * once its branch is merged or dropped (as git draws branches). */
export interface FlowLanes {
  /** lane of each flow ('' = main = 0) */
  lane: Map<string, number>;
  count: number;
  /** first and last row of each flow */
  span: Map<string, [number, number]>;
}

export function flowLanes(entries: AuditEntry[]): FlowLanes {
  const span = new Map<string, [number, number]>();
  const extend = (f: string, i: number) => {
    const s = span.get(f);
    span.set(f, s ? [Math.min(s[0], i), Math.max(s[1], i)] : [i, i]);
  };
  entries.forEach((e, i) => {
    extend(e.flow, i);
    if (e.fork) extend(e.fork, i);
    if (e.merge) extend(e.merge, i);
  });
  extend('', 0);
  extend('', Math.max(entries.length - 1, 0));
  const lane = new Map<string, number>([['', 0]]);
  const used: [number, number][][] = [[]];
  const flows = [...span.entries()].filter(([f]) => f !== '').sort((a, b) => a[1][0] - b[1][0]);
  for (const [f, [a, b]] of flows) {
    let l = 1;
    while (used[l]?.some(([x, y]) => a <= y && b >= x)) l++;
    (used[l] ??= []).push([a, b]);
    lane.set(f, l);
  }
  return { lane, count: Math.max(1, ...[...lane.values()].map((l) => l + 1)), span };
}
