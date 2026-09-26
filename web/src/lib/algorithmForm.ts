// Editing model of the algorithms of a domain (ADR 0018): algorithms (script + declared
// parameters), their instances (parameter values), and where instances are plugged.
import type { Algorithm, AlgorithmInstance, AlgorithmParam, AlgorithmParamType } from './api';
import { ALGORITHM_TEMPLATES, ALGORITHM_USAGES, type AlgorithmUsage } from './dsl';
import { newUid } from './methodologyForm';

export const PARAM_TYPES: AlgorithmParamType[] = ['string', 'number', 'boolean', 'regex', 'enum', 'strings', 'json', 'secret'];

/** hint of a secret value: a reference resolved by the hub at call time, never the secret itself */
export const SECRET_HINT = 'env:VARIABLE  or  <vault path>#<field>';
export const ALGORITHM_LANGUAGES = ['javascript', 'go'] as const;
export { ALGORITHM_USAGES };

export interface ParamForm {
  name: string;
  type: string;
  description: string;
  required: boolean;
  /** text form of the default ("" : none); a list is comma-separated, json is JSON text */
  defaultValue: string;
  /** enum values, comma-separated */
  values: string;
}

export interface AlgorithmForm {
  /** local id, never sent: the tab survives renames */
  uid: string;
  name: string;
  description: string;
  type: string;
  language: string;
  code: string;
  params: ParamForm[];
  /** adapters only */
  mcp: string;
  connector: string;
}

export interface InstanceForm {
  uid: string;
  name: string;
  description: string;
  algorithm: string;
  /** typed values by parameter name */
  values: Record<string, unknown>;
}

const csv = (v: string): string[] =>
  v
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);

export function defaultToText(type: string, v: unknown): string {
  if (v === undefined || v === null) return '';
  if (type === 'strings' && Array.isArray(v)) return v.join(', ');
  if (type === 'json') return JSON.stringify(v);
  return String(v);
}

/** Converts the text of a default / a control into the typed value of a parameter (undefined: unset). */
export function textToValue(type: string, text: string): unknown {
  const t = text.trim();
  if (t === '') return undefined;
  switch (type) {
    case 'number': {
      const n = Number(t);
      return Number.isNaN(n) ? t : n;
    }
    case 'boolean':
      return t === 'true' ? true : t === 'false' ? false : t;
    case 'strings':
      return csv(t);
    case 'json':
      try {
        return JSON.parse(t) as unknown;
      } catch {
        return t;
      }
    default:
      return text;
  }
}

export function algorithmToForm(a: Algorithm): AlgorithmForm {
  return {
    uid: newUid(),
    name: a.name ?? '',
    description: a.description ?? '',
    type: a.type ?? 'property_validator',
    language: a.language ?? 'javascript',
    code: a.code ?? '',
    mcp: a.mcp ?? '',
    connector: a.connector ?? '',
    params: (a.params ?? []).map((p) => ({
      name: p.name ?? '',
      type: p.type ?? 'string',
      description: p.description ?? '',
      required: !!p.required,
      defaultValue: defaultToText(p.type ?? 'string', p.defaultValue),
      values: (p.values ?? []).join(', '),
    })),
  };
}

export function algorithmFromForm(a: AlgorithmForm): Algorithm {
  const o: Algorithm = { name: a.name.trim(), type: a.type, language: a.language, code: a.code };
  if (a.description.trim()) o.description = a.description.trim();
  if (a.type === 'adapter') {
    o.mcp = a.mcp.trim();
    o.connector = a.connector.trim();
  }
  if (a.params.length) {
    o.params = a.params.map((p) => {
      const q: AlgorithmParam = { name: p.name.trim(), type: p.type };
      if (p.description.trim()) q.description = p.description.trim();
      if (p.required) q.required = true;
      const def = textToValue(p.type, p.defaultValue);
      if (def !== undefined) q.defaultValue = def;
      if (p.type === 'enum' && csv(p.values).length) q.values = csv(p.values);
      return q;
    });
  }
  return o;
}

export function instanceToForm(i: AlgorithmInstance): InstanceForm {
  return { uid: newUid(), name: i.name ?? '', description: i.description ?? '', algorithm: i.algorithm ?? '', values: { ...(i.values ?? {}) } };
}

export function instanceFromForm(i: InstanceForm): AlgorithmInstance {
  const o: AlgorithmInstance = { name: i.name.trim(), algorithm: i.algorithm.trim() };
  if (i.description.trim()) o.description = i.description.trim();
  const values: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(i.values)) if (v !== undefined && v !== '') values[k] = v;
  if (Object.keys(values).length) o.values = values;
  return o;
}

export function emptyAlgorithm(type: AlgorithmUsage = 'property_validator', name = ''): AlgorithmForm {
  return { uid: newUid(), name, description: '', type, language: 'javascript', code: ALGORITHM_TEMPLATES[type].javascript, params: [], mcp: '', connector: '' };
}

export const emptyParam = (): ParamForm => ({ name: '', type: 'string', description: '', required: false, defaultValue: '', values: '' });

export const emptyInstance = (algorithm = '', name = ''): InstanceForm => ({ uid: newUid(), name, description: '', algorithm, values: {} });

/** A free name derived from base ("regex", "regex-2"…). */
export function freeName(base: string, taken: string[]): string {
  let name = base;
  for (let k = 2; taken.includes(name); k++) name = `${base}-${k}`;
  return name;
}

/** Sample input of "Try it" for a usage, as pretty JSON. */
export function sampleInput(type: string): string {
  const u = ALGORITHM_USAGES.find((x) => x.usage === type);
  return JSON.stringify(u?.sample ?? {}, null, 2);
}

/** Where an instance is plugged in a domain form. */
export function plugsOf(
  instance: string,
  nodeTypes: { name: string; validators: { property: string; instance: string }[] }[],
  lifecycles: { name: string; transitions: { name: string; from: string; to: string; guards: string[]; actions: string[] }[] }[],
): string[] {
  const out: string[] = [];
  for (const n of nodeTypes) for (const v of n.validators) if (v.instance === instance) out.push(`${n.name || '(unnamed)'}.${v.property} · validator`);
  for (const l of lifecycles)
    for (const t of l.transitions) {
      if (t.guards.includes(instance)) out.push(`${l.name} › ${t.name} (${t.from} → ${t.to}) · guard`);
      if (t.actions.includes(instance)) out.push(`${l.name} › ${t.name} (${t.from} → ${t.to}) · action`);
    }
  return out;
}
