// The review of the description of a change (ADR 0110): an action of the methodology rates the title and the intent
// with items of kind assessment (each the whole record of its key, the latest in effect is the rating), and the
// person answers in the thread of the rating with items of kind comment. Mirrors the server's pkg/assess.
import type { ChangeItem, Process } from './api';

export const DESCRIPTION = 'description';
export const MAX_SCORE = 5;
/** from this score the description is accepted (condition.AcceptableScore) */
export const ACCEPTABLE_SCORE = 3;

export interface Finding {
  text: string;
  severity: string;
}

export interface Assessment {
  key: string;
  score: number;
  verdict: string;
  findings: Finding[];
  suggestion: string;
  reply: string;
  rated: string;
  /** the description still reads as rated */
  current: boolean;
  /** no comment came after the rating */
  answered: boolean;
  versions: number;
  /** the action that rated it */
  by: string;
  /** the item of the rating */
  item: string;
}

export interface Message {
  kind: 'comment' | 'assessment';
  id: string;
  text: string;
  score?: number;
  at?: string;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');
const num = (v: unknown) => (typeof v === 'number' ? v : Number(v) || 0);
const dataOf = (it: ChangeItem) => (it.data ?? {}) as Record<string, unknown>;

/** subjectText is the text a rating of the description is about: the title, a blank line, the intent. */
export function subjectText(change: { title?: string; intent?: string }): string {
  return `${(change.title ?? '').trim()}\n\n${(change.intent ?? '').trim()}`;
}

/** fingerprint is the short fingerprint of a subject text a rater may copy in `rated` (FNV-1a 32 bits over UTF-8, the
 *  twin of assess.Fingerprint). */
export function fingerprint(text: string): string {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(text)) {
    h ^= b;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16).padStart(8, '0');
}

/** rates says whether a rating made on `rated` (the text or its fingerprint) is about `text`. */
export function rates(rated: string, text: string): boolean {
  const r = rated.trim();
  return r === text.trim() || r === fingerprint(text);
}

function inEffect(items: ChangeItem[]): ChangeItem[] {
  const superseded = new Set(items.flatMap((it) => it.supersedes ?? []));
  return items.filter((it) => it.status !== 'rejected' && it.status !== 'superseded' && !superseded.has(it.id ?? ''));
}

/** assessmentOf is the latest rating of a subject of the change, undefined when none. */
export function assessmentOf(change: { title?: string; intent?: string; items?: ChangeItem[] }, key = DESCRIPTION): Assessment | undefined {
  let last: ChangeItem | undefined;
  let at = -1;
  let commented = -1;
  let versions = 0;
  inEffect(change.items ?? []).forEach((it, i) => {
    const d = dataOf(it);
    if (it.kind === 'comment' && str(d.about) === key) commented = i;
    if (it.kind === 'assessment' && str(d.key) === key) {
      last = it;
      at = i;
      versions++;
    }
  });
  if (!last) return undefined;
  const d = dataOf(last);
  const findings = (Array.isArray(d.findings) ? d.findings : []).map((f) =>
    typeof f === 'string' ? { text: f, severity: '' } : { text: str((f as Record<string, unknown>)?.text), severity: str((f as Record<string, unknown>)?.severity) },
  );
  const rated = str(d.rated);
  return {
    key,
    score: num(d.score),
    verdict: str(d.verdict),
    findings,
    suggestion: str(d.suggestion),
    reply: str(d.reply),
    rated,
    current: key === DESCRIPTION ? rates(rated, subjectText(change)) : true,
    answered: commented < at,
    versions,
    by: last.producedBy ?? '',
    item: last.id ?? '',
  };
}

/** threadOf lists the messages about a subject, oldest first: the comments and the ratings (their reply). */
export function threadOf(items: ChangeItem[] | undefined, key = DESCRIPTION): Message[] {
  const out: Message[] = [];
  for (const it of inEffect(items ?? [])) {
    const d = dataOf(it);
    if (it.kind === 'comment' && str(d.about) === key) out.push({ kind: 'comment', id: it.id ?? '', text: str(d.text), at: it.createdAt });
    else if (it.kind === 'assessment' && str(d.key) === key)
      out.push({ kind: 'assessment', id: it.id ?? '', text: str(d.reply) || str(d.verdict), score: num(d.score), at: it.createdAt });
  }
  return out;
}

/** accepted mirrors description_accepted: a rating of the current text with a score from ACCEPTABLE_SCORE. */
export const accepted = (a: Assessment | undefined) => !!a && a.current && a.score >= ACCEPTABLE_SCORE;

/** state is what the bubble says of the rating. */
export function reviewState(a: Assessment | undefined): 'none' | 'stale' | 'awaiting' | 'accepted' | 'refused' {
  if (!a) return 'none';
  if (!a.current) return 'stale';
  if (!a.answered) return 'awaiting';
  return a.score >= ACCEPTABLE_SCORE ? 'accepted' : 'refused';
}

/** raterRun is the run of the reaction that rated the description: the run whose step wrote the rating item (the
 *  agent of the method, a model or a person, ADR 0110), up to the companion run of the reaction it runs under. */
export function raterRun(processes: Iterable<Process>, change: { id?: string }, item: string): Process | undefined {
  if (!change.id || !item) return undefined;
  const byId = new Map<string, Process>();
  let found: Process | undefined;
  for (const p of processes) {
    if (p.id) byId.set(p.id, p);
    if (p.changeId === change.id && p.steps?.some((s) => s.items?.includes(item))) found = p;
  }
  for (let hops = 0; found?.parentId && byId.has(found.parentId) && hops < 16; hops++) found = byId.get(found.parentId);
  return found;
}

/** commentItem is the item a person writes to answer the rater. */
export const commentItem = (text: string, key = DESCRIPTION): ChangeItem => ({ kind: 'comment', type: 'comment', data: { about: key, text: text.trim() } });
