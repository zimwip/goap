// The value of a code attribute (contracts/codetext): a front matter naming the language, then the text as a fenced
// Markdown code block. Same reading and writing as the server, which checks it on every write.
import type { CodeLanguage } from './codemirror';

export interface CodeValue {
  /** language of the text (go, python, c…), '' when unknown */
  language: string;
  /** the other keys of the front matter, kept as they come */
  meta: Record<string, string>;
  /** the source text, without the fences */
  code: string;
}

const KEY = /^[A-Za-z][A-Za-z0-9_.-]*$/;

/** The languages the editor highlights, by the names the git importer gives them. */
export const CODE_LANGUAGES: { value: string; label: string }[] = [
  { value: 'c', label: 'C' },
  { value: 'cpp', label: 'C++' },
  { value: 'go', label: 'Go' },
  { value: 'java', label: 'Java' },
  { value: 'javascript', label: 'JavaScript' },
  { value: 'python', label: 'Python' },
  { value: 'rust', label: 'Rust' },
  { value: 'tsx', label: 'TSX' },
  { value: 'typescript', label: 'TypeScript' },
];

const ALIASES: Record<string, CodeLanguage> = {
  h: 'c', 'c++': 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp', golang: 'go', py: 'python', js: 'javascript', jsx: 'javascript', mjs: 'javascript',
  ts: 'typescript', rs: 'rust',
};
const HIGHLIGHTED = new Set<string>(['c', 'cpp', 'go', 'java', 'javascript', 'python', 'rust', 'tsx', 'typescript']);

/** The language of the editor for the language of a value ('text' when it highlights none). */
export function editorLanguage(l: string): CodeLanguage {
  const k = l.trim().toLowerCase();
  if (HIGHLIGHTED.has(k)) return k as CodeLanguage;
  return ALIASES[k] ?? 'text';
}

export const languageLabel = (l: string): string => CODE_LANGUAGES.find((x) => x.value === l)?.label ?? l;

function longestRun(s: string, c: string): number {
  let best = 0;
  let run = 0;
  for (const ch of s) {
    run = ch === c ? run + 1 : 0;
    if (run > best) best = run;
  }
  return best;
}

const needsQuotes = (v: string) => v === '' || v.trim() !== v || /[\n\r"#]/.test(v) || v.startsWith("'");
const infoString = (l: string) => (/[\s`]/.test(l) ? '' : l);

/** The fenced code block of a value alone (no front matter), its fence longer than any run of backticks of the text. */
export function codeBlock(v: Pick<CodeValue, 'language' | 'code'>): string {
  const fence = '`'.repeat(Math.max(3, longestRun(v.code, '`') + 1));
  const code = v.code.replace(/\n$/, '');
  return `${fence}${infoString(v.language)}\n${code ? `${code}\n` : ''}${fence}\n`;
}

/** Writes a value: the front matter (the language first, then the other keys in order), then the code block. */
export function formatCode(v: Partial<CodeValue>): string {
  const language = v.language ?? '';
  let out = '---\n';
  const key = (k: string, val: string) => (out += `${k}: ${needsQuotes(val) ? JSON.stringify(val) : val}\n`);
  if (language) key('language', language);
  for (const k of Object.keys(v.meta ?? {}).sort()) if (k !== 'language' && KEY.test(k)) key(k, v.meta![k]);
  return `${out}---\n${codeBlock({ language, code: v.code ?? '' })}`;
}

function openFence(line: string): { char: string; n: number; info: string } | null {
  const m = /^(`{3,}|~{3,})(.*)$/.exec(line);
  if (!m) return null;
  const info = m[2].trim();
  if (m[1][0] === '`' && info.includes('`')) return null;
  return { char: m[1][0], n: m[1].length, info: info.split(' ')[0] };
}

function closeFence(line: string, char: string, n: number): boolean {
  const l = line.replace(/[ \t]+$/, '');
  return l.length >= n && [...l].every((c) => c === char);
}

function unquote(val: string): string {
  if (val.startsWith('"')) return JSON.parse(val) as string;
  if (val.length >= 2 && val.startsWith("'") && val.endsWith("'")) return val.slice(1, -1).replaceAll("''", "'");
  return val;
}

/** Reads a code value; throws on a front matter not closed or malformed, or text after the block. A plain text is code of no known language. */
export function parseCode(s: string): CodeValue {
  const v: CodeValue = { language: '', meta: {}, code: '' };
  const lines = s.replaceAll('\r\n', '\n').split('\n');
  let i = 0;
  if (lines[0] === '---') {
    let closed = false;
    for (i = 1; i < lines.length; i++) {
      const line = lines[i];
      if (line === '---') {
        closed = true;
        i++;
        break;
      }
      if (!line.trim() || line.trim().startsWith('#')) continue;
      const at = line.indexOf(':');
      const k = at < 0 ? '' : line.slice(0, at).trim();
      if (!KEY.test(k)) throw new Error(`front matter line ${i + 1}: a key: value is expected, got “${line}”`);
      let val: string;
      try {
        val = unquote(line.slice(at + 1).trim());
      } catch {
        throw new Error(`front matter line ${i + 1}: bad quoted value`);
      }
      if (k === 'language') v.language = val;
      else v.meta[k] = val;
    }
    if (!closed) throw new Error('the front matter is not closed by a --- line');
    while (i < lines.length - 1 && !lines[i].trim()) i++;
  }
  if (i >= lines.length) return v;
  const rest = lines.slice(i);
  const f = openFence(rest[0]);
  if (!f) return { ...v, code: rest.join('\n') };
  if (!v.language) v.language = f.info;
  const end = rest.findIndex((l, j) => j > 0 && closeFence(l, f.char, f.n));
  if (end < 0) return { ...v, code: rest.slice(1).join('\n') };
  if (rest.slice(end + 1).join('\n').trim()) throw new Error('text after the code block');
  return { ...v, code: rest.slice(1, end).join('\n') };
}

/** Reads a value without throwing: a value that does not read is shown as plain text. */
export function readCode(s: string): CodeValue & { error?: string } {
  try {
    return parseCode(s);
  } catch (e) {
    return { language: '', meta: {}, code: s, error: e instanceof Error ? e.message : String(e) };
  }
}
