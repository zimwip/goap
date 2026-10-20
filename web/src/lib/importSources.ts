// The Import from a source tab (ADR 0148): the parameters of an importer as the domain declares them, turned into a form
// and back into the values of a run, and how a run reads.

import type { Importer, ImportJob, JsonValue } from './api';

/** A field of the form of an importer: one declared parameter. */
export interface ParamField {
  name: string;
  label: string;
  description: string;
  kind: 'text' | 'number' | 'boolean' | 'enum';
  /** a directory on the machine of the importer service: the form offers the chooser (widget `directory`) */
  directory: boolean;
  /** the values of an enum parameter */
  options: { value: string; label: string }[];
  default: string;
}

/** The fields of an importer, in the order the domain gives them. */
export function paramFields(im: Importer): ParamField[] {
  return (im.params ?? [])
    .map((p, i) => ({ p, i }))
    .sort((a, b) => (a.p.attribute?.order ?? 0) - (b.p.attribute?.order ?? 0) || a.i - b.i)
    .map(({ p }) => {
      const a = p.attribute ?? {};
      const kind: ParamField['kind'] = a.type === 'number' ? 'number' : a.type === 'boolean' ? 'boolean' : a.type === 'enum' ? 'enum' : 'text';
      return {
        name: a.name ?? '',
        label: a.label || a.name || '',
        description: a.description || a.tooltip || '',
        kind,
        directory: a.widget === 'directory',
        options: (p.values ?? []).map((v) => ({ value: v.value ?? '', label: v.label || v.value || '' })),
        default: a.defaultValue ?? '',
      };
    });
}

/** The values a form starts from: the defaults (a boolean as a boolean). */
export function initialValues(fields: ParamField[]): Record<string, string | boolean> {
  const out: Record<string, string | boolean> = {};
  for (const f of fields) out[f.name] = f.kind === 'boolean' ? f.default === 'true' : f.default;
  return out;
}

/** The parameters of a run from the values of the form: empty ones left out, numbers as numbers. */
export function runParams(fields: ParamField[], values: Record<string, string | boolean>): Record<string, JsonValue> | string {
  const out: Record<string, JsonValue> = {};
  for (const f of fields) {
    const v = values[f.name];
    if (f.kind === 'boolean') {
      if (v === true) out[f.name] = true;
      continue;
    }
    const s = typeof v === 'string' ? v.trim() : '';
    if (!s) continue;
    if (f.kind === 'number') {
      const n = Number(s);
      if (!Number.isFinite(n)) return `${f.label}: a number is expected`;
      out[f.name] = n;
    } else out[f.name] = s;
  }
  return out;
}

/** A job runs an importer, of the domain of `namespace` when one is named. */
export function isImportJob(j: ImportJob, namespace = ''): boolean {
  return (j.kind ?? '').startsWith('import.') && (!namespace || j.target === namespace);
}

export { statsLine } from './jobView';

/** What a run imported from, for the list of runs (its first text parameter, the repository for git). */
export function jobSubject(j: ImportJob): string {
  const p = (j.params ?? {}) as Record<string, JsonValue>;
  for (const k of ['repository', 'directory', 'url', 'source', 'path', 'name']) if (typeof p[k] === 'string' && p[k]) return p[k] as string;
  return '';
}
