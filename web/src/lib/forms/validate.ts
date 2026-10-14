// The checks of a form, the same as the engine's (pkg/form.Validate) minus what only the server knows: run before
// sending, so a person sees the issues by their fields. Pure.
import type { FieldDef, FormDef, FormValues, LinkValue, NodeOption, NodeSource } from './types';

/** Widgets that show something and produce no value. */
export const DISPLAY_WIDGETS = new Set(['markdown']);

export const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []);
const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
const bool = (v: unknown): boolean => v === true;

export function isEmpty(v: unknown): boolean {
  return v === undefined || v === null || (typeof v === 'string' && v.trim() === '') || (Array.isArray(v) && v.length === 0);
}

/** The values a form starts with: the defaults of its fields. */
export function initialValues(form: FormDef | undefined): FormValues {
  const out: FormValues = {};
  for (const f of form?.fields ?? []) if (f.default !== undefined && !DISPLAY_WIDGETS.has(f.widget)) out[f.name] = structuredClone(f.default);
  return out;
}

/** The fields grouped by section, in order of first appearance ('' : no section). */
export function sections(form: FormDef | undefined): { name: string; fields: FieldDef[] }[] {
  const out: { name: string; fields: FieldDef[] }[] = [];
  for (const f of form?.fields ?? []) {
    const name = f.section ?? '';
    let s = out.find((x) => x.name === name);
    if (!s) out.push((s = { name, fields: [] }));
    s.fields.push(f);
  }
  return out;
}

/** Whether a node satisfies the type / state / namespace / change constraints of a field. */
export function nodeAllowed(n: NodeOption, c: Record<string, unknown> | undefined, source: NodeSource, prefix = ''): boolean {
  const isA = source.isA ?? ((t: string, a: string) => t === a);
  const types = strings(c?.[prefix ? `${prefix}Types` : 'types']);
  const states = strings(c?.[prefix ? `${prefix}States` : 'states']);
  if (types.length && !types.some((t) => isA(n.type, t))) return false;
  if (states.length && !states.includes(n.state ?? '')) return false;
  if (!prefix) {
    const ns = c?.['namespace'];
    if (typeof ns === 'string' && ns && n.namespace !== ns) return false;
    if (bool(c?.['inChange']) && !n.inChange) return false;
  }
  return true;
}

/** The nodes a node field offers. */
export const nodeChoices = (f: FieldDef, source: NodeSource): NodeOption[] => source.nodes.filter((n) => nodeAllowed(n, f.constraints, source));

/** The targets a link field offers. */
export const linkTargets = (f: FieldDef, source: NodeSource): NodeOption[] => source.nodes.filter((n) => nodeAllowed(n, f.constraints, source, 'to'));

/** The sources of a link field: its fixed key, else the nodes chosen in its from field. */
export function linkSources(f: FieldDef, values: FormValues): string[] {
  const key = f.constraints?.['fromKey'];
  if (typeof key === 'string' && key) return [key];
  const ref = f.constraints?.['from'];
  if (typeof ref !== 'string') return [];
  const v = values[ref];
  return typeof v === 'string' ? (v ? [v] : []) : strings(v);
}

/** The link types a link field offers from a source: its constraint, else what the type catalogue allows. */
export function linkTypes(f: FieldDef, sourceType: string | undefined, source: NodeSource): string[] {
  const declared = strings(f.constraints?.['linkTypes']);
  if (declared.length) return declared;
  return sourceType && source.linkTypesFrom ? source.linkTypesFrom(sourceType) : [];
}

/** The links of a link field with their source and type completed when the field fixes them. */
export function completeLinks(f: FieldDef, v: unknown, values: FormValues): LinkValue[] {
  const list = Array.isArray(v) ? (v as LinkValue[]) : [];
  const sources = linkSources(f, values);
  const types = strings(f.constraints?.['linkTypes']);
  return list.map((l) => ({ ...l, from: l.from || (sources.length === 1 ? sources[0] : ''), type: l.type || (types.length === 1 ? types[0] : '') }));
}

function count(f: FieldDef, n: number, what: string): string[] {
  const out: string[] = [];
  const min = num(f.constraints?.['min']);
  const max = num(f.constraints?.['max']);
  if (min !== undefined && n < min) out.push(`at least ${min} ${what}`);
  if (max !== undefined && n > max) out.push(`at most ${max} ${what}`);
  return out;
}

/** The issues of one field's value (empty: none). Node keys are checked against the source when it knows them. */
export function fieldIssues(f: FieldDef, value: unknown, values: FormValues, source: NodeSource): string[] {
  if (DISPLAY_WIDGETS.has(f.widget)) return [];
  const v = isEmpty(value) ? f.default : value;
  if (isEmpty(v)) return f.required ? ['required'] : [];
  const c = f.constraints ?? {};
  const out: string[] = [];
  const byKey = new Map(source.nodes.map((n) => [n.key, n]));
  const nodeIssue = (key: string, prefix = ''): string | undefined => {
    const n = byKey.get(key);
    if (!n) return source.nodes.length ? `unknown node ${key}` : undefined;
    return nodeAllowed(n, c, source, prefix) ? undefined : `${key} is not allowed here`;
  };
  switch (f.widget) {
    case 'text':
    case 'textarea': {
      if (typeof v !== 'string') return ['expects a text'];
      const n = [...v].length;
      const min = num(c['minLength']);
      const max = num(c['maxLength']);
      if (min !== undefined && n < min) out.push(`at least ${min} characters`);
      if (max !== undefined && n > max) out.push(`at most ${max} characters`);
      if (typeof c['pattern'] === 'string') {
        try {
          if (!new RegExp(`^(?:${c['pattern']})$`).test(v)) out.push(`does not match ${c['pattern']}`);
        } catch {
          /* the server reports an invalid pattern */
        }
      }
      return out;
    }
    case 'number': {
      if (typeof v !== 'number' || !Number.isFinite(v)) return ['expects a number'];
      const min = num(c['min']);
      const max = num(c['max']);
      if (min !== undefined && v < min) out.push(`at least ${min}`);
      if (max !== undefined && v > max) out.push(`at most ${max}`);
      if (bool(c['integer']) && !Number.isInteger(v)) out.push('a whole number');
      return out;
    }
    case 'boolean':
      return typeof v === 'boolean' ? [] : ['expects true or false'];
    case 'date': {
      if (typeof v !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(v)) return ['expects a date YYYY-MM-DD'];
      if (typeof c['min'] === 'string' && v < c['min']) out.push(`on or after ${c['min']}`);
      if (typeof c['max'] === 'string' && v > c['max']) out.push(`on or before ${c['max']}`);
      return out;
    }
    case 'select':
      return typeof v === 'string' && strings(c['options']).includes(v) ? [] : [`${String(v)} is not one of the options`];
    case 'multiselect': {
      const list = strings(v);
      for (const s of list) if (!strings(c['options']).includes(s)) out.push(`${s} is not one of the options`);
      return [...out, ...count(f, list.length, 'values')];
    }
    case 'node': {
      const keys = bool(c['multiple']) ? strings(v) : typeof v === 'string' ? [v] : [];
      for (const k of keys) {
        const msg = nodeIssue(k);
        if (msg) out.push(msg);
      }
      return bool(c['multiple']) ? [...out, ...count(f, keys.length, 'nodes')] : out;
    }
    case 'link': {
      const links = completeLinks(f, v, values);
      const sources = linkSources(f, values);
      const types = strings(c['linkTypes']);
      links.forEach((l, i) => {
        const at = `link ${i + 1}: `;
        if (!l.from || !l.type || !l.to) out.push(`${at}needs a source, a type and a target`);
        else if (!sources.includes(l.from)) out.push(`${at}no source node is chosen`);
        else if (types.length && !types.includes(l.type)) out.push(`${at}${l.type} is not allowed`);
        else {
          const msg = nodeIssue(l.to, 'to');
          if (msg) out.push(at + msg);
        }
      });
      return [...out, ...count(f, links.length, 'links')];
    }
  }
  return out;
}

/** The issues of every field, by field name. */
export function formIssues(form: FormDef | undefined, values: FormValues, source: NodeSource): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const f of form?.fields ?? []) {
    const is = fieldIssues(f, values[f.name], values, source);
    if (is.length) out[f.name] = is;
  }
  return out;
}

/** The values to send: blanks dropped, links completed with their source and type. */
export function submittedValues(form: FormDef | undefined, values: FormValues): FormValues {
  const out: FormValues = {};
  for (const f of form?.fields ?? []) {
    if (DISPLAY_WIDGETS.has(f.widget)) continue;
    const v = values[f.name];
    if (isEmpty(v)) continue;
    out[f.name] = f.widget === 'link' ? completeLinks(f, v, values) : v;
  }
  return out;
}
