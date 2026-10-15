// The assessments of a change (ADR 0110): an action of the methodology rates a subject (the description, the title,
// the intent, an impact, an item) with items of type assessment (each the whole record of its key, the latest in
// effect is the rating), and the person answers in the thread of the rating with items of type comment. Mirrors the
// server's pkg/assess.
import type { AssessmentState, ChangeItem, Process } from './api';

export const DESCRIPTION = 'description';
export const TITLE = 'title';
export const INTENT = 'intent';
/** the subject of the impact of a node, by the key of the node */
export const impactSubject = (nodeKey: string) => `impact:${nodeKey}`;
/** the subjects of an item: by the key of its record, then by its id (a rater may name either) */
export const itemSubjects = (it: { id?: string; data?: unknown }): string[] => {
  const k = str((it.data as Record<string, unknown> | undefined)?.key);
  return [...(k ? [`item:${k}`] : []), ...(it.id ? [`item:${it.id}`] : [])];
};
/** Subject is a subject key, or the keys naming one subject (the first is the one a comment is about). */
export type Subject = string | string[];
const keysOf = (s: Subject) => (Array.isArray(s) ? s : [s]);
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

/** typeOf is the type of an item, an assessment or a comment a model wrapped as an artifact of that target counting as
 *  one (the engine unwraps them since, ADR 0110; older changes keep them). */
export function typeOf(it: ChangeItem): string {
  const k = it.type ?? '';
  return k === 'artifact' && (it.target === 'assessment' || it.target === 'comment') ? it.target : k;
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

/** textOf is the text of a subject the web can rebuild as the server does (assess.Text): the description, the title and
 *  the intent; undefined for an impact or an item, whose canonical text needs the graph. */
function textOf(change: { title?: string; intent?: string }, key: string): string | undefined {
  if (key === DESCRIPTION) return subjectText(change);
  if (key === TITLE) return (change.title ?? '').trim();
  if (key === INTENT) return (change.intent ?? '').trim();
  return undefined;
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
export function assessmentOf(change: { title?: string; intent?: string; items?: ChangeItem[] }, subject: Subject = DESCRIPTION): Assessment | undefined {
  const keys = keysOf(subject);
  const key = keys[0] ?? '';
  let last: ChangeItem | undefined;
  let at = -1;
  let commented = -1;
  let versions = 0;
  inEffect(change.items ?? []).forEach((it, i) => {
    const d = dataOf(it);
    if (typeOf(it) === 'comment' && keys.includes(str(d.about))) commented = i;
    if (typeOf(it) === 'assessment' && keys.includes(str(d.key))) {
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
  const text = textOf(change, str(d.key));
  return {
    key,
    score: num(d.score),
    verdict: str(d.verdict),
    findings,
    suggestion: str(d.suggestion),
    reply: str(d.reply),
    rated,
    current: text === undefined || rates(rated, text),
    answered: commented < at,
    versions,
    by: last.producedBy ?? '',
    item: last.id ?? '',
  };
}

/** threadOf lists the messages about a subject, oldest first: the comments and the ratings (their reply). */
export function threadOf(items: ChangeItem[] | undefined, subject: Subject = DESCRIPTION): Message[] {
  const keys = keysOf(subject);
  const out: Message[] = [];
  for (const it of inEffect(items ?? [])) {
    const d = dataOf(it);
    if (typeOf(it) === 'comment' && keys.includes(str(d.about))) out.push({ kind: 'comment', id: it.id ?? '', text: str(d.text), at: it.createdAt });
    else if (typeOf(it) === 'assessment' && keys.includes(str(d.key)))
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
export const commentItem = (text: string, subject: Subject = DESCRIPTION): ChangeItem => ({
  type: 'comment',
  target: 'comment',
  data: { about: keysOf(subject)[0] ?? DESCRIPTION, text: text.trim() },
});

/** pending says whether the rating still waits to be processed: the bubble floats open by the description until the
 *  rating is accepted (a low score, a remark not answered yet, a description changed since its rating), or while the
 *  rater works on a description it has not rated yet. */
export function pending(st: ReturnType<typeof reviewState>, working = false, processed?: boolean): boolean {
  if (processed !== undefined) return !processed && (st !== 'none' || working);
  return st === 'none' ? working : st !== 'accepted';
}

/** bubbleKey identifies what the bubble shows: hiding it lasts until the rating, its thread or the text changes. */
export const bubbleKey = (a: Assessment | undefined, thread: Message[]): string =>
  `${a?.item ?? ''}|${a?.current ? 1 : 0}|${thread.length}`;

/** applicable is the wording the person may apply to the subject, the suggestion as rated or as they edited it:
 *  trimmed, '' when it is empty or says what the subject already says. */
export function applicable(text: string, value?: string): string {
  const t = text.trim();
  return t && t !== (value ?? '').trim() ? t : '';
}

/** The tones of an assessment state (ADR 0116). */
export const TONES = ['ok', 'warn', 'danger', 'info', 'neutral'] as const;
export type Tone = (typeof TONES)[number];

/** How the bubble of an assessment reads: its tone, what it says, whether it opens by itself. */
export interface BubbleView {
  tone: Tone;
  label: string;
  open: boolean;
  /** the state comes from the methodology, evaluated by the server */
  declared: boolean;
}

const LABELS: Record<ReturnType<typeof reviewState>, string> = {
  none: 'not rated yet',
  stale: 'changed since its rating',
  awaiting: 'your remark awaits an answer',
  accepted: 'accepted',
  refused: 'to be improved',
};

/** bubbleView is the state of the bubble: the one the server evaluated from what the methodology declares (ADR 0116)
 *  for the rating shown, else the local judgement (a score of ACCEPTABLE_SCORE or more, or `processed` as the caller
 *  judges it), used while the states load or when they lag behind the rating. The bubble opens on a click; by itself
 *  only when the methodology declares a state `open`. */
export function bubbleView(rating: Assessment | undefined, states: AssessmentState[] | undefined, processed?: boolean): BubbleView {
  const s = rating ? states?.find((x) => x.subject === rating.key) : undefined;
  if (rating && s && (s.score ?? 0) === rating.score && !!s.current === rating.current) {
    const tone = (TONES as readonly string[]).includes(s.tone ?? '') ? (s.tone as Tone) : 'neutral';
    return { tone, label: s.label || s.verdict || tone, open: !!s.open, declared: true };
  }
  const st = reviewState(rating);
  const due = processed !== undefined ? !processed && !!rating : st !== 'accepted' && st !== 'none';
  const label = processed === undefined || !rating ? LABELS[st] : processed ? 'processed' : 'not processed yet';
  return { tone: due ? 'warn' : rating ? 'ok' : 'neutral', label, open: false, declared: false };
}

/** The context key under which a change view gives its bubbles the evaluated states: a getter of the list. */
export const ASSESSMENT_STATES = Symbol('assessmentStates');
export type AssessmentStatesContext = () => AssessmentState[] | undefined;
