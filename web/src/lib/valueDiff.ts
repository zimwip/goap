// The diff of two values of an attribute (ValueDiff.svelte): what changed between a version and the next, a draft and
// its base, two baselines. The attribute names how (`diff` of its domain definition, else the usual one of its type,
// the twin of `def.Attribute.DefaultDiff`): the two values whole, the words of a text, its lines, indented JSON, or
// source text highlighted in its language (codeText.ts). Pure: the component only lays the rows out.
import { readCode } from './codeText';

/** How a change of a value is shown (def.AttributeDiffs). */
export type DiffMode = 'value' | 'words' | 'lines' | 'json' | 'code';
export const DIFF_MODES: readonly DiffMode[] = ['value', 'words', 'lines', 'json', 'code'];

/** The diff of an attribute: the one it names, else the usual one of its type and widget (def.Attribute.DefaultDiff). */
export function defaultDiff(a: { type?: string; widget?: string; diff?: string } | undefined): DiffMode {
  if (a?.diff && (DIFF_MODES as readonly string[]).includes(a.diff)) return a.diff as DiffMode;
  switch (a?.type) {
    case 'code':
      return 'code';
    case 'json':
      return 'json';
    case 'number':
    case 'boolean':
    case 'date':
    case 'enum':
      return 'value';
  }
  if (a?.widget === 'code') return 'code';
  if (a?.widget === 'textarea') return 'lines';
  return 'words';
}

export type Op = 'same' | 'add' | 'del';

/** The shortest edit script of two sequences (Myers), as the operation of each element in order: the common prefix
 * and suffix are cut first, so a small change in a long text stays cheap. */
export function diffSeq<T>(a: readonly T[], b: readonly T[], eq: (x: T, y: T) => boolean = (x, y) => x === y): { op: Op; a?: number; b?: number }[] {
  let start = 0;
  while (start < a.length && start < b.length && eq(a[start], b[start])) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && eq(a[endA - 1], b[endB - 1])) endA--, endB--;
  const head = Array.from({ length: start }, (_, i) => ({ op: 'same' as Op, a: i, b: i }));
  const tail = Array.from({ length: a.length - endA }, (_, i) => ({ op: 'same' as Op, a: endA + i, b: endB + i }));
  const n = endA - start;
  const m = endB - start;
  const mid: { op: Op; a?: number; b?: number }[] = [];
  if (!n || !m) {
    for (let i = 0; i < n; i++) mid.push({ op: 'del', a: start + i });
    for (let j = 0; j < m; j++) mid.push({ op: 'add', b: start + j });
    return [...head, ...mid, ...tail];
  }
  // forward Myers, keeping each round's frontier to walk the path back
  const max = n + m;
  const off = max;
  let v: Int32Array = new Int32Array(2 * max + 2);
  const trace: Int32Array[] = [];
  let found = false;
  for (let d = 0; d <= max && !found; d++) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]) ? v[off + k + 1] : v[off + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && eq(a[start + x], b[start + y])) x++, y++;
      v[off + k] = x;
      if (x >= n && y >= m) {
        found = true;
        break;
      }
    }
  }
  trace.push(v);
  let x = n;
  let y = m;
  // trace[d]: the frontier after round d - 1, from which round d stepped
  for (let d = trace.length - 2; d > 0; d--) {
    v = trace[d];
    const k = x - y;
    const down = k === -d || (k !== d && v[off + k - 1] < v[off + k + 1]);
    const pk = down ? k + 1 : k - 1;
    const px = v[off + pk];
    const py = px - pk;
    while (x > px && y > py) mid.push({ op: 'same', a: start + --x, b: start + --y });
    if (down) mid.push({ op: 'add', b: start + --y });
    else mid.push({ op: 'del', a: start + --x });
  }
  while (x > 0 && y > 0) mid.push({ op: 'same', a: start + --x, b: start + --y });
  while (x > 0) mid.push({ op: 'del', a: start + --x });
  while (y > 0) mid.push({ op: 'add', b: start + --y });
  mid.reverse();
  return [...head, ...mid, ...tail];
}

/** A piece of text, kept, added or removed. */
export interface Segment {
  op: Op;
  text: string;
}

const WORD = /[\p{L}\p{N}_]+|\s+|[^\p{L}\p{N}_\s]/gu;
export const words = (s: string): string[] => s.match(WORD) ?? [];

/** The words (and spaces, and signs) added and removed from `a` to `b`, adjacent pieces of one kind joined. */
export function wordDiff(a: string, b: string): Segment[] {
  const wa = words(a);
  const wb = words(b);
  const out: Segment[] = [];
  for (const s of diffSeq(wa, wb)) {
    const text = s.op === 'add' ? wb[s.b!] : wa[s.a!];
    const last = out.at(-1);
    if (last?.op === s.op) last.text += text;
    else out.push({ op: s.op, text });
  }
  return out;
}

/** One line of a line diff: its text, and its number on each side it is on (from 1). */
export interface LineRow {
  op: Op;
  text: string;
  old?: number;
  new?: number;
  /** a removed line set against the added one in its place (or the reverse): the words that changed between them */
  words?: Segment[];
}

export const lines = (s: string): string[] => (s === '' ? [] : s.replaceAll('\r\n', '\n').replace(/\n$/, '').split('\n'));

/** The lines of `a` and `b`, kept, removed and added. `pairWords`: a run of removed lines followed by a run of added
 * ones are set against each other line by line, with the words that changed. */
export function lineDiff(a: string, b: string, pairWords = true): LineRow[] {
  const la = lines(a);
  const lb = lines(b);
  const rows: LineRow[] = diffSeq(la, lb).map((s) =>
    s.op === 'same' ? { op: 'same', text: la[s.a!], old: s.a! + 1, new: s.b! + 1 } : s.op === 'del' ? { op: 'del', text: la[s.a!], old: s.a! + 1 } : { op: 'add', text: lb[s.b!], new: s.b! + 1 },
  );
  if (pairWords) for (const [d, ad] of runs(rows)) for (let i = 0; i < Math.min(d.length, ad.length); i++) pairRow(rows[d[i]], rows[ad[i]]);
  return rows;
}

function pairRow(del: LineRow, add: LineRow) {
  const w = wordDiff(del.text, add.text);
  // lines with nothing in common are a removal and an addition, not an edit
  if (!w.some((s) => s.op === 'same' && s.text.trim())) return;
  del.words = w.filter((s) => s.op !== 'add');
  add.words = w.filter((s) => s.op !== 'del');
}

/** The runs of removed lines each followed by a run of added lines: their indexes. */
function runs(rows: LineRow[]): [number[], number[]][] {
  const out: [number[], number[]][] = [];
  for (let i = 0; i < rows.length; ) {
    if (rows[i].op !== 'del') {
      i++;
      continue;
    }
    const d: number[] = [];
    const ad: number[] = [];
    while (i < rows.length && rows[i].op === 'del') d.push(i++);
    while (i < rows.length && rows[i].op === 'add') ad.push(i++);
    out.push([d, ad]);
  }
  return out;
}

/** Unchanged lines left out between two changes: rows [from, to) of the diff. */
export interface Fold {
  fold: true;
  from: number;
  to: number;
}

/** The rows to show: the changes with `context` unchanged lines around them, the longer unchanged runs folded
 * (`open`: the folds, by their first row, shown anyway). */
export function foldRows(rows: LineRow[], context = 3, open: ReadonlySet<number> = new Set()): (LineRow | Fold)[] {
  const keep = rows.map((r) => r.op !== 'same');
  const near = keep.slice();
  rows.forEach((_, i) => {
    if (!keep[i]) return;
    for (let j = Math.max(0, i - context); j <= Math.min(rows.length - 1, i + context); j++) near[j] = true;
  });
  const out: (LineRow | Fold)[] = [];
  for (let i = 0; i < rows.length; ) {
    if (near[i]) {
      out.push(rows[i++]);
      continue;
    }
    let j = i;
    while (j < rows.length && !near[j]) j++;
    // folding a line or two hides nothing worth a click
    if (j - i <= 2 || open.has(i)) out.push(...rows.slice(i, j));
    else out.push({ fold: true, from: i, to: j });
    i = j;
  }
  return out;
}

export const isFold = (r: LineRow | SplitRow | Fold): r is Fold => 'fold' in r;

/** Side by side: each row has the line of the old value on the left, of the new one on the right. */
export interface SplitRow {
  left?: LineRow;
  right?: LineRow;
}

/** The rows set side by side: a kept line on both sides, the removed lines against the added ones that follow them. */
export function splitRows(rows: (LineRow | Fold)[]): (SplitRow | Fold)[] {
  const out: (SplitRow | Fold)[] = [];
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    if (isFold(r) || r.op === 'same') {
      out.push(isFold(r) ? r : { left: r, right: r });
      i++;
      continue;
    }
    const d: LineRow[] = [];
    const ad: LineRow[] = [];
    while (i < rows.length && !isFold(rows[i]) && (rows[i] as LineRow).op === 'del') d.push(rows[i++] as LineRow);
    while (i < rows.length && !isFold(rows[i]) && (rows[i] as LineRow).op === 'add') ad.push(rows[i++] as LineRow);
    for (let k = 0; k < Math.max(d.length, ad.length); k++) out.push({ left: d[k], right: ad[k] });
  }
  return out;
}

/** The lines added and removed. */
export function counts(rows: LineRow[]): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const r of rows) r.op === 'add' ? added++ : r.op === 'del' ? removed++ : 0;
  return { added, removed };
}

/** A value as JSON indented, its object keys sorted, so that two values differing by key order compare equal. */
export function jsonText(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'string') {
    try {
      const parsed = JSON.parse(v);
      if (parsed && typeof parsed === 'object') return jsonText(parsed);
    } catch {
      // a plain text stays as it is
    }
    return v;
  }
  return JSON.stringify(
    v,
    (_k, x) => (x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x as Record<string, unknown>).sort(([p], [q]) => p.localeCompare(q))) : x),
    2,
  );
}

/** A value as text: a string as it is, anything else as JSON; absent: ''. */
export const plainText = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

/** What the diff shows of two values: the text of each side, compared as `mode`, and the language of code. */
export interface DiffSides {
  mode: DiffMode;
  before: string;
  after: string;
  /** code: the language of the newer side that names one */
  language: string;
  /** code: the front matter keys whose value changed (the language included), `key: old → new` */
  meta: string[];
}

/** The text compared of two values. A code value is compared on its source text, its front matter apart. */
export function diffSides(mode: DiffMode, before: unknown, after: unknown): DiffSides {
  if (mode === 'json') return { mode, before: jsonText(before), after: jsonText(after), language: '', meta: [] };
  if (mode !== 'code') return { mode, before: plainText(before), after: plainText(after), language: '', meta: [] };
  const b = typeof before === 'string' && before ? readCode(before) : undefined;
  const a = typeof after === 'string' && after ? readCode(after) : undefined;
  const meta: string[] = [];
  if (b && a) {
    const mb = { language: b.language, ...b.meta } as Record<string, string>;
    const ma = { language: a.language, ...a.meta } as Record<string, string>;
    for (const k of [...new Set([...Object.keys(mb), ...Object.keys(ma)])])
      if ((mb[k] ?? '') !== (ma[k] ?? '')) meta.push(`${k}: ${mb[k] || '—'} → ${ma[k] || '—'}`);
  }
  return { mode, before: b?.code ?? plainText(before), after: a?.code ?? plainText(after), language: a?.language || b?.language || '', meta };
}

/** Is the change short enough to read on one line (old → new) rather than as a block of lines? */
export function isInline(s: DiffSides, max = 120): boolean {
  if (s.mode === 'code') return false;
  const short = (t: string) => t.length <= max && !t.includes('\n');
  return short(s.before) && short(s.after);
}
