// What a read query (ADR 0160) may say next where the cursor stands, from the type catalogue of the registry: the node
// types after `(x:`, the link types after `[:` that a node of the type on the left can hold (its outgoing or entering
// links, by the arrow), the node types a link reaches on the right, the attributes after `x.` or `{` of the type
// the variable was given, else the keywords, functions and variables. Pure: the editor asks it at each keystroke.
import { splitType, type TypeCatalog } from './stores/types.svelte';

export type SuggestionKind = 'type' | 'link' | 'property' | 'keyword' | 'variable' | 'function';

export interface Suggestion {
  /** the text matched against what is typed and inserted */
  label: string;
  kind: SuggestionKind;
  detail?: string;
  info?: string;
  /** a higher rank comes first */
  boost?: number;
}

export interface Suggestions {
  /** where the completed word starts */
  from: number;
  options: Suggestion[];
}

export interface QueryScope {
  cat: TypeCatalog;
  /** the namespace the query reads: its types are named bare, the others with their namespace */
  namespace: string;
  /** variables bound before the match (`n`, the node a pane queries from) → their node type */
  bound?: Record<string, string>;
}

/** The node's own fields a query reads before its properties (graph/graph/query.go, qNode.Property). */
export const NODE_FIELDS = ['key', 'type', 'state', 'id', 'version', 'namespace', 'owner', 'project'];
const LINK_FIELDS = ['type', 'id'];

const KEYWORDS = [
  'MATCH', 'OPTIONAL MATCH', 'WHERE', 'RETURN', 'DISTINCT', 'AS', 'ORDER BY', 'SKIP', 'LIMIT', 'ASC', 'DESC',
  'AND', 'OR', 'XOR', 'NOT', 'IN', 'STARTS WITH', 'ENDS WITH', 'CONTAINS', 'IS NULL', 'IS NOT NULL', 'EXISTS',
  'CASE', 'WHEN', 'THEN', 'ELSE', 'END', 'true', 'false', 'null',
];
const FUNCTIONS = [
  'id', 'type', 'labels', 'keys', 'properties', 'nodes', 'relationships', 'length', 'size', 'head', 'last', 'toLower',
  'toUpper', 'trim', 'toString', 'toInteger', 'toFloat', 'abs', 'coalesce', 'count', 'collect', 'sum', 'avg', 'min', 'max',
];

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;
// a label or a link type as typed: a name, or an escaped name possibly not closed yet
const NAME_PART = '(`[^`]*`?|[A-Za-z_][A-Za-z0-9_]*)?';
// a pattern variable given a label: `(r:Requirement`, `[l:satisfies`
const LABELLED = /([([])\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*)/g;

/** How a query names a type: bare in the namespace it reads, else escaped with its namespace. */
export function nameOf(ref: string, namespace: string): string {
  const { namespace: ns, name } = splitType(ref);
  if (ns === namespace && IDENT.test(name)) return name;
  return '`' + ref.replaceAll('`', '``') + '`';
}

const unescape = (s: string) => (s.startsWith('`') ? s.slice(1, s.endsWith('`') && s.length > 1 ? -1 : undefined).replaceAll('``', '`') : s);

/** The type a label of a query names: qualified as is, else of the namespace read, else the one type of that name. */
function typeOfLabel(label: string, scope: QueryScope, link: boolean): string {
  const l = unescape(label);
  if (l.includes('@')) return l;
  const known = link ? scope.cat.links.map((x) => x.ref ?? '') : [...scope.cat.types.keys()];
  const own = `${scope.namespace}@${l}`;
  if (known.includes(own)) return own;
  return known.find((r) => splitType(r).name === l) ?? '';
}

/** The variables of a query with the type their pattern gives them (nodes and links apart), the bound ones first. */
export function variableTypes(text: string, scope: QueryScope): { nodes: Map<string, string>; links: Map<string, string> } {
  const nodes = new Map<string, string>(Object.entries(scope.bound ?? {}));
  const links = new Map<string, string>();
  for (const m of stripStrings(text).matchAll(LABELLED)) {
    const link = m[1] === '[';
    const t = typeOfLabel(m[3], scope, link);
    if (t) (link ? links : nodes).set(m[2], t);
  }
  return { nodes, links };
}

/** The variables a query names: those of its patterns, its aliases and the bound ones. */
function variablesOf(text: string, scope: QueryScope): string[] {
  const s = stripStrings(text);
  const out = new Set(Object.keys(scope.bound ?? {}));
  for (const m of s.matchAll(/[([]\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?=[:)\]{\s])/g)) out.add(m[1]);
  for (const m of s.matchAll(/\b([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\(/g)) out.add(m[1]);
  for (const m of s.matchAll(/\bAS\s+([A-Za-z_][A-Za-z0-9_]*)/gi)) out.add(m[1]);
  return [...out].filter((v) => !KEYWORDS.some((k) => k.toLowerCase() === v.toLowerCase()));
}

/** The text with the contents of its string literals blanked (same length), so brackets and colons in them count for nothing. */
function stripStrings(text: string): string {
  let out = '';
  let quote = '';
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quote) {
      if (c === '\\') {
        out += '  ';
        i++;
        continue;
      }
      if (c === quote) quote = '';
      out += c === quote || !quote ? c : ' ';
      continue;
    }
    if (c === "'" || c === '"') quote = c;
    out += c;
  }
  return out;
}

const inString = (before: string) => {
  let quote = '';
  for (let i = 0; i < before.length; i++) {
    const c = before[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
    } else if (c === "'" || c === '"') quote = c;
  }
  return quote !== '';
};

/** The innermost bracket left open before the cursor: its character and its position. */
function openBracket(s: string): { ch: string; at: number } | undefined {
  const stack: { ch: string; at: number }[] = [];
  let tick = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === '`') tick = !tick;
    if (tick || c === '`') continue;
    if (c === '(' || c === '[' || c === '{') stack.push({ ch: c, at: i });
    else if (c === ')' || c === ']' || c === '}') stack.pop();
  }
  return stack.at(-1);
}

/** The node pattern ending right before `end` (`(a:Type)` of `(a:Type)-[`): its variable and label. */
function nodeBefore(s: string, end: number): { v: string; label: string } | undefined {
  const m = /\(\s*([A-Za-z_][A-Za-z0-9_]*)?\s*(?::\s*(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*))?[^()]*\)\s*$/.exec(s.slice(0, end));
  return m ? { v: m[1] ?? '', label: m[2] ?? '' } : undefined;
}

function typeOfNode(p: { v: string; label: string } | undefined, vars: Map<string, string>, scope: QueryScope): string {
  if (!p) return '';
  if (p.label) return typeOfLabel(p.label, scope, false);
  return vars.get(p.v) ?? '';
}

function typeOptions(refs: string[], scope: QueryScope, boost = 0): Suggestion[] {
  return refs.map((ref) => ({
    label: nameOf(ref, scope.namespace),
    kind: 'type' as const,
    detail: splitType(ref).namespace,
    info: scope.cat.type(ref)?.description || undefined,
    boost: boost + (splitType(ref).namespace === scope.namespace ? 1 : 0),
  }));
}

function linkOptions(refs: { ref: string; boost: number; detail: string }[], scope: QueryScope): Suggestion[] {
  return refs.map(({ ref, boost, detail }) => ({
    label: nameOf(ref, scope.namespace),
    kind: 'link' as const,
    detail,
    info: linkInfo(scope, ref),
    boost: boost + (splitType(ref).namespace === scope.namespace ? 1 : 0),
  }));
}

/** How the domain reads a link type, when it says more than its name. */
function linkInfo(scope: QueryScope, ref: string): string | undefined {
  const l = scope.cat.links.find((x) => x.ref === ref);
  const name = splitType(ref).name;
  const parts = [l?.label, l?.inverseLabel].filter((x) => x && x !== name);
  return parts.length ? `${name}: ${l?.label || name}${l?.inverseLabel && l.inverseLabel !== l.label ? ` / ${l.inverseLabel}` : ''}` : undefined;
}

/** The link types a node of the type holds: outgoing ones (`out`), entering ones (`in`), or both, outgoing first. */
function linksAt(scope: QueryScope, type: string, dir: 'out' | 'in' | 'both') {
  const { cat } = scope;
  const all = cat.links.filter((l) => l.ref);
  if (!type) return all.map((l) => ({ ref: l.ref ?? '', boost: 0, detail: ends(l.from, l.to) }));
  const types = cat.typesOf(type);
  const out = dir !== 'in' ? all.filter((l) => !l.from || types.includes(l.from)) : [];
  const into = dir !== 'out' ? all.filter((l) => cat.linkAccepts(l, type)) : [];
  const seen = new Set<string>();
  const res: { ref: string; boost: number; detail: string }[] = [];
  // a link naming the type's end ranks before one whose end is open (any type), an outgoing one before an entering one
  for (const l of out) {
    seen.add(l.ref ?? '');
    res.push({ ref: l.ref ?? '', boost: l.from ? 4 : 0, detail: `→ ${l.to ? splitType(l.to).name : 'any'}` });
  }
  for (const l of into) {
    if (seen.has(l.ref ?? '')) continue;
    res.push({ ref: l.ref ?? '', boost: l.to ? (dir === 'in' ? 4 : 3) : 0, detail: `← ${l.from ? splitType(l.from).name : 'any'}` });
  }
  return res;
}

const ends = (from?: string, to?: string) => `${from ? splitType(from).name : 'any'} → ${to ? splitType(to).name : 'any'}`;

/** The node types at the other end of links of these types: their targets (`out`), their sources (`in`), or both; none when every end is open. */
function typesAcross(scope: QueryScope, links: string[], dir: 'out' | 'in' | 'both'): string[] | undefined {
  const { cat } = scope;
  const out = new Set<string>();
  for (const ref of links) {
    const l = cat.links.find((x) => x.ref === ref);
    if (!l) continue;
    const sides = dir === 'out' ? [l.to] : dir === 'in' ? [l.from] : [l.to, l.from];
    // an open end reaches any type: it ranks none of them first
    for (const side of sides) if (side) for (const t of cat.withSubtypes(side)) out.add(t);
  }
  return out.size ? [...out].sort() : undefined;
}

/** What may come at `pos` of a query; null where nothing fits (a string literal, nothing typed and not asked). */
export function cypherSuggestions(text: string, pos: number, scope: QueryScope, explicit = false): Suggestions | null {
  const raw = text.slice(0, pos);
  if (inString(raw)) return null;
  const before = stripStrings(raw);
  const vars = variableTypes(text, scope);

  // x.prop: the fields and attributes of the variable's type
  const dot = /([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)?$/.exec(before);
  if (dot) {
    const from = pos - (dot[2]?.length ?? 0);
    const link = vars.links.get(dot[1]);
    if (link) return { from, options: propertyOptions(scope, link, true) };
    return { from, options: propertyOptions(scope, vars.nodes.get(dot[1]) ?? '', false) };
  }

  const open = openBracket(before);
  const inner = open ? before.slice(open.at + 1) : '';

  // (x:Type or (x:A:B: a node type, read from the link on its left when there is one
  if (open?.ch === '(') {
    const label = new RegExp(`^\\s*([A-Za-z_][A-Za-z0-9_]*)?\\s*(?::\\s*(?:\`[^\`]+\`|[A-Za-z_][A-Za-z0-9_]*)\\s*)*:\\s*${NAME_PART}$`).exec(inner);
    if (label) {
      const typed = label[2] ?? '';
      return { from: pos - typed.length, options: nodeTypesAt(before, open.at, vars, scope) };
    }
    if (/^\s*[A-Za-z_][A-Za-z0-9_]*\s*$/.test(inner) || /^\s*$/.test(inner)) return words(before, pos, text, scope, explicit, false);
  }

  // [l:type or [:a|b: a link type the node on the left can hold
  if (open?.ch === '[') {
    const label = new RegExp(`^\\s*([A-Za-z_][A-Za-z0-9_]*)?\\s*:\\s*(?:(?:\`[^\`]+\`|[A-Za-z_][A-Za-z0-9_]*)\\s*\\|\\s*:?\\s*)*${NAME_PART}$`).exec(inner);
    if (label) {
      const typed = label[2] ?? '';
      const arrow = /(<-|-)\s*$/.exec(before.slice(0, open.at));
      const left = arrow ? nodeBefore(before, open.at - arrow[0].length) : undefined;
      const dir = arrow?.[1] === '<-' ? 'in' : 'both';
      return { from: pos - typed.length, options: linkOptions(linksAt(scope, typeOfNode(left, vars.nodes, scope), dir), scope) };
    }
  }

  // (x:Type { prop: the attributes of the pattern's type
  if (open?.ch === '{') {
    const head = /\(\s*([A-Za-z_][A-Za-z0-9_]*)?\s*(?::\s*(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*))?\s*$/.exec(before.slice(0, open.at));
    const rel = /\[\s*([A-Za-z_][A-Za-z0-9_]*)?\s*(?::\s*(`[^`]+`|[A-Za-z_][A-Za-z0-9_]*))?\s*$/.exec(before.slice(0, open.at));
    const key = /(?:^|[{,])\s*([A-Za-z_][A-Za-z0-9_]*)?$/.exec(inner);
    if ((head || rel) && key) {
      const from = pos - (key[1]?.length ?? 0);
      if (rel) return { from, options: propertyOptions(scope, rel[2] ? typeOfLabel(rel[2], scope, true) : '', true) };
      return { from, options: propertyOptions(scope, typeOfNode({ v: head?.[1] ?? '', label: head?.[2] ?? '' }, vars.nodes, scope), false) };
    }
  }

  return words(before, pos, text, scope, explicit, true);
}

/** The node types a node pattern opened at `at` may name: across the link on its left, else every type. */
function nodeTypesAt(before: string, at: number, vars: { nodes: Map<string, string>; links: Map<string, string> }, scope: QueryScope): Suggestion[] {
  const all = scope.cat.names();
  const rel = /(<-|-)\s*\[\s*([A-Za-z_][A-Za-z0-9_]*)?\s*(?::([^\]]*))?\]\s*(->|-)\s*$/.exec(before.slice(0, at));
  if (rel) {
    const dir = rel[1] === '<-' ? 'in' : rel[4] === '->' ? 'out' : 'both';
    let links = (rel[3] ?? '')
      .split('|')
      .map((x) => x.trim().replace(/^:/, '').trim())
      .filter(Boolean)
      .map((x) => typeOfLabel(x, scope, true))
      .filter(Boolean);
    if (!links.length && rel[2]) links = [vars.links.get(rel[2]) ?? ''].filter(Boolean);
    if (!links.length) {
      // no type named: the link types the node on the left holds that way
      const left = nodeBefore(before, rel.index);
      const type = typeOfNode(left, vars.nodes, scope);
      if (type) links = linksAt(scope, type, dir === 'in' ? 'in' : dir === 'out' ? 'out' : 'both').map((l) => l.ref);
    }
    const across = links.length ? typesAcross(scope, links, dir) : undefined;
    if (across) return [...typeOptions(across, scope, 4), ...typeOptions(all.filter((t) => !across.includes(t)), scope)];
  }
  return typeOptions(all, scope);
}

function propertyOptions(scope: QueryScope, type: string, link: boolean): Suggestion[] {
  const fields = (link ? LINK_FIELDS : NODE_FIELDS).map((f) => ({ label: f, kind: 'property' as const, detail: link ? 'link' : 'node', boost: 1 }));
  const attrs = link ? (scope.cat.links.find((l) => l.ref === type)?.attributes ?? []) : scope.cat.attributes(type);
  const own = attrs
    .map((a) => a.attribute)
    .filter((a) => a?.name && IDENT.test(a.name))
    .map((a) => ({
      label: a?.name ?? '',
      kind: 'property' as const,
      detail: [a?.type, a?.label && a.label !== a.name ? a.label : ''].filter(Boolean).join(' · ') || splitType(type).name,
      info: a?.description || undefined,
      boost: 3,
    }));
  const names = new Set(own.map((o) => o.label));
  return [...own, ...fields.filter((f) => !names.has(f.label))];
}

/** A word outside a pattern: keywords, then the variables and functions where an expression may stand. */
function words(before: string, pos: number, text: string, scope: QueryScope, explicit: boolean, expr: boolean): Suggestions | null {
  const w = /[A-Za-z_][A-Za-z0-9_]*$/.exec(before);
  const typed = w?.[0] ?? '';
  if (!typed && !explicit) return null;
  const from = pos - typed.length;
  const options: Suggestion[] = [];
  const own = new Set(variablesOf(text.slice(0, from) + text.slice(pos), scope));
  if (expr) {
    for (const k of KEYWORDS) options.push({ label: k, kind: 'keyword' });
    for (const f of FUNCTIONS) options.push({ label: f, kind: 'function', detail: 'function' });
  }
  for (const v of own) {
    const t = variableTypes(text, scope);
    const type = t.nodes.get(v) ?? t.links.get(v);
    options.push({ label: v, kind: 'variable', detail: type ? splitType(type).name : undefined, boost: 2 });
  }
  return options.length ? { from, options } : null;
}
