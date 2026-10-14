// The model of the form designer (ADR 0115): the palette of widgets, the patterns, and the edits of a form (add a
// field where it is dropped, move, remove, rename keeping the references, patch). Pure: the designer component holds
// no logic of its own.
import { WIDGET_CATALOG } from './widgetCatalog.gen';
import type { Condition, ConstraintSpec, FieldDef, FormDef, WidgetSpec } from './types';

/** The widgets the palette offers, in the order a person reaches for them. */
const ORDER = ['text', 'textarea', 'number', 'boolean', 'date', 'select', 'multiselect', 'node', 'link', 'group', 'markdown'];

export const LABELS: Record<string, string> = {
  text: 'Text',
  textarea: 'Long text',
  number: 'Number',
  boolean: 'Yes / no',
  date: 'Date',
  select: 'Choice',
  multiselect: 'Choices',
  node: 'Node search',
  link: 'Links',
  group: 'Repeated group',
  markdown: 'Explanation',
};

export function palette(catalog: WidgetSpec[] = WIDGET_CATALOG): WidgetSpec[] {
  const rank = (n: string) => (ORDER.includes(n) ? ORDER.indexOf(n) : ORDER.length);
  return [...catalog].sort((a, b) => rank(a.name) - rank(b.name) || a.name.localeCompare(b.name));
}

export const specOf = (widget: string, catalog: WidgetSpec[] = WIDGET_CATALOG): WidgetSpec | undefined => catalog.find((w) => w.name === widget);

export const constraintsOf = (widget: string, catalog: WidgetSpec[] = WIDGET_CATALOG): ConstraintSpec[] => specOf(widget, catalog)?.constraints ?? [];

export const labelOf = (widget: string): string => LABELS[widget] ?? widget;

/** A field name not taken in the form, from a base. */
export function uniqueName(form: FormDef, base: string): string {
  const taken = new Set((form.fields ?? []).map((f) => f.name));
  const clean = base.replace(/[^A-Za-z0-9_]/g, '_').replace(/^[^A-Za-z]+/, '') || 'field';
  if (!taken.has(clean)) return clean;
  for (let i = 2; ; i++) if (!taken.has(`${clean}${i}`)) return `${clean}${i}`;
}

/** The field a widget starts as: a free name, its label, the constraints it requires. */
export function newField(form: FormDef, widget: string, catalog: WidgetSpec[] = WIDGET_CATALOG): FieldDef {
  const f: FieldDef = { name: uniqueName(form, widget), widget, label: labelOf(widget) };
  const c: Record<string, unknown> = {};
  for (const k of constraintsOf(widget, catalog)) {
    if (!k.required) continue;
    if (k.type === 'strings') c[k.name] = widget === 'select' || widget === 'multiselect' ? ['option 1', 'option 2'] : [];
    else if (k.type === 'string') c[k.name] = widget === 'markdown' ? 'Explain what to do.' : '';
  }
  if (widget === 'link') {
    const source = (form.fields ?? []).find((x) => x.widget === 'node');
    if (source) c['from'] = source.name;
  }
  if (Object.keys(c).length) f.constraints = c;
  if (widget === 'markdown') delete f.label;
  if (widget === 'group') f.fields = [{ name: 'item', widget: 'text', label: 'Item', required: true }];
  return f;
}

const fieldsOf = (form: FormDef): FieldDef[] => form.fields ?? [];

/** The form with a field inserted at index (the end when out of range). */
export function insertField(form: FormDef, field: FieldDef, index = fieldsOf(form).length): FormDef {
  const fields = [...fieldsOf(form)];
  fields.splice(Math.max(0, Math.min(index, fields.length)), 0, field);
  return { ...form, fields };
}

/** The form with the field at `from` moved to `to` (the index it is dropped before, counted before the move). */
export function moveField(form: FormDef, from: number, to: number): FormDef {
  const fields = [...fieldsOf(form)];
  if (from < 0 || from >= fields.length) return form;
  const [f] = fields.splice(from, 1);
  const at = to > from ? to - 1 : to;
  fields.splice(Math.max(0, Math.min(at, fields.length)), 0, f);
  return { ...form, fields };
}

/** The form without the field at index; the link fields that took their source from it lose that source. */
export function removeField(form: FormDef, index: number): FormDef {
  const gone = fieldsOf(form)[index];
  if (!gone) return form;
  const fields = fieldsOf(form)
    .filter((_, i) => i !== index)
    .map((f) => {
      const g = withConditions(f, (cs) => cs.filter((c) => c.field !== gone.name));
      return g.constraints?.['from'] === gone.name ? setConstraint(g, 'from', undefined) : g;
    });
  return { ...form, fields };
}

/** The form with the field at index patched; a rename follows into the fields naming it. */
export function updateField(form: FormDef, index: number, patch: Partial<FieldDef>): FormDef {
  const old = fieldsOf(form)[index];
  if (!old) return form;
  const next = { ...old, ...patch };
  const renamed = patch.name !== undefined && patch.name !== old.name ? old.name : undefined;
  const fields = fieldsOf(form).map((f, i) => {
    if (i === index || !renamed) return i === index ? next : f;
    const g = withConditions(f, (cs) => cs.map((c) => (c.field === renamed ? { ...c, field: next.name } : c)));
    return g.constraints?.['from'] === renamed ? { ...g, constraints: { ...g.constraints, from: next.name } } : g;
  });
  return { ...form, fields };
}

/** The field with its conditions (shown when, required when) rewritten; an emptied list goes. */
function withConditions(f: FieldDef, edit: (cs: Condition[]) => Condition[]): FieldDef {
  const out = { ...f };
  for (const k of ['visibleWhen', 'requiredWhen'] as const) {
    if (!f[k]) continue;
    const cs = edit(f[k]);
    if (cs.length) out[k] = cs;
    else delete out[k];
  }
  return out;
}

/** The operator of a condition, as the designer shows it. */
export type ConditionOp = 'equals' | 'in' | 'set' | 'unset';

export const opOf = (c: Condition): ConditionOp => (c.set !== undefined ? (c.set ? 'set' : 'unset') : c.in !== undefined ? 'in' : 'equals');

/** A condition on a field with another operator, keeping what it can of the value. */
export function withOp(c: Condition, op: ConditionOp): Condition {
  const first = c.equals !== undefined ? c.equals : c.in?.[0];
  switch (op) {
    case 'set':
    case 'unset':
      return { field: c.field, set: op === 'set' };
    case 'in':
      return { field: c.field, in: first !== undefined ? [first] : [] };
    default:
      return { field: c.field, equals: first ?? '' };
  }
}

/** The problems of the conditions of the i-th field of a list. */
export function conditionIssues(fields: FieldDef[], i: number): string[] {
  const out: string[] = [];
  const f = fields[i];
  for (const [k, what] of [['visibleWhen', 'shown when'], ['requiredWhen', 'required when']] as const) {
    for (const c of f[k] ?? []) {
      const n = [c.equals !== undefined, c.in !== undefined, c.set !== undefined].filter(Boolean).length;
      if (n !== 1) out.push(`${what}: a condition tests exactly one of equals, in or set`);
      const at = fields.findIndex((x) => x.name === c.field);
      if (at < 0 || at >= i) out.push(`${what}: no field ${c.field || '(none)'} before this one`);
      else if (['markdown', 'link', 'group'].includes(fields[at].widget)) out.push(`${what}: ${c.field} has no value to test`);
    }
  }
  return out;
}

/** The constraints of a field with one set (undefined, '' or [] removes it). */
export function setConstraint(field: FieldDef, name: string, value: unknown): FieldDef {
  const c = { ...(field.constraints ?? {}) };
  if (value === undefined || value === '' || (Array.isArray(value) && value.length === 0)) delete c[name];
  else c[name] = value;
  const out = { ...field };
  if (Object.keys(c).length) out.constraints = c;
  else delete out.constraints;
  return out;
}

/** The field with another widget: the constraints the new one does not accept are dropped. */
export function changeWidget(field: FieldDef, widget: string, catalog: WidgetSpec[] = WIDGET_CATALOG): FieldDef {
  const accepted = new Set(constraintsOf(widget, catalog).map((c) => c.name));
  const c = Object.fromEntries(Object.entries(field.constraints ?? {}).filter(([k]) => accepted.has(k)));
  const out: FieldDef = { ...field, widget };
  if (Object.keys(c).length) out.constraints = c;
  else delete out.constraints;
  delete out.default;
  if (widget === 'group') out.fields ??= newField({}, 'group', catalog).fields;
  else delete out.fields;
  return out;
}

/** The problems of a form definition the server would refuse (pkg/form.CheckForm), by field index ('form' for the
 * form). */
export function designIssues(form: FormDef, catalog: WidgetSpec[] = WIDGET_CATALOG): Record<string, string[]> {
  return fieldsDesignIssues(fieldsOf(form), catalog, false);
}

function fieldsDesignIssues(fields: FieldDef[], catalog: WidgetSpec[], nested: boolean): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  const push = (k: string, m: string) => (out[k] ??= []).push(m);
  const seen = new Set<string>();
  fields.forEach((f, i) => {
    const k = String(i);
    if (!/^[A-Za-z][A-Za-z0-9_]*$/.test(f.name)) push(k, 'the name is a letter, then letters, digits or _');
    else if (seen.has(f.name)) push(k, `another field is named ${f.name}`);
    seen.add(f.name);
    for (const m of conditionIssues(fields, i)) push(k, m);
    const spec = specOf(f.widget, catalog);
    if (!spec) {
      push(k, `unknown widget ${f.widget}`);
      return;
    }
    if (nested && (f.widget === 'group' || f.widget === 'link')) push(k, `a group holds no ${labelOf(f.widget).toLowerCase()} field`);
    if (f.widget === 'group') {
      if (!f.fields?.length) push(k, 'a group needs at least one sub-field');
      for (const [j, msgs] of Object.entries(fieldsDesignIssues(f.fields ?? [], catalog, true)))
        for (const m of msgs) push(k, `sub-field ${f.fields?.[Number(j)]?.name || Number(j) + 1}: ${m}`);
    } else if (f.fields?.length) push(k, 'only a group has sub-fields');
    for (const c of spec.constraints ?? []) {
      const v = f.constraints?.[c.name];
      if (c.required && (v === undefined || v === '' || (Array.isArray(v) && !v.length))) push(k, `${c.name} is required`);
      if (c.type === 'field' && typeof v === 'string' && v) {
        const other = fields.find((x) => x.name === v && x !== f);
        if (!other) push(k, `no field ${v}`);
        else if (c.widget && other.widget !== c.widget) push(k, `${v} is not a ${labelOf(c.widget).toLowerCase()} field`);
      }
    }
    for (const key of Object.keys(f.constraints ?? {})) if (!spec.constraints?.some((c) => c.name === key)) push(k, `${spec.name} has no constraint ${key}`);
    if (f.widget === 'link' && !f.constraints?.['from'] && !f.constraints?.['fromKey']) push(k, 'choose the source: a node field or a fixed key');
  });
  return out;
}

/** Reads the JSON of a form ('' : an empty form). */
export function parseForm(json: string): { form?: FormDef; error?: string } {
  if (!json.trim()) return { form: { fields: [] } };
  try {
    const f: unknown = JSON.parse(json);
    if (f === null || typeof f !== 'object' || Array.isArray(f)) return { error: 'a JSON object is expected' };
    const form = f as FormDef;
    return { form: { ...form, fields: Array.isArray(form.fields) ? form.fields : [] } };
  } catch (e) {
    return { error: e instanceof Error ? e.message : String(e) };
  }
}

/** The JSON of a form ('' : a form that declares nothing, so the action keeps none). */
export function formJSON(form: FormDef): string {
  const out: FormDef = {};
  for (const k of ['title', 'description', 'submit', 'artifact'] as const) if (form[k]?.trim()) out[k] = form[k];
  if (form.fields?.length) out.fields = form.fields;
  return Object.keys(out).length ? JSON.stringify(out, null, 2) : '';
}

/** A pattern: a ready-made set of fields to start from. */
export interface Pattern {
  name: string;
  label: string;
  description: string;
  form: FormDef;
}

export const PATTERNS: Pattern[] = [
  {
    name: 'pick-and-link',
    label: 'Pick and link',
    description: 'Choose a node, then the nodes it links to, and say why.',
    form: {
      submit: 'Link',
      fields: [
        { name: 'node', widget: 'node', label: 'Node', required: true },
        { name: 'links', widget: 'link', label: 'Links', constraints: { from: 'node' } },
        { name: 'why', widget: 'textarea', label: 'Why', required: true },
      ],
    },
  },
  {
    name: 'qualify',
    label: 'Qualify a node',
    description: 'Choose a node of the change, give a verdict and its justification.',
    form: {
      submit: 'Qualify',
      fields: [
        { name: 'node', widget: 'node', label: 'Node', required: true, constraints: { inChange: true } },
        { name: 'verdict', widget: 'select', label: 'Verdict', required: true, constraints: { options: ['confirmed', 'refuted'] } },
        { name: 'justification', widget: 'textarea', label: 'Justification', required: true, constraints: { minLength: 10 } },
      ],
    },
  },
  {
    name: 'decide',
    label: 'Decide',
    description: 'Choose among options and give the rationale.',
    form: {
      submit: 'Decide',
      fields: [
        { name: 'decision', widget: 'select', label: 'Decision', required: true, constraints: { options: ['go', 'no go'] } },
        { name: 'rationale', widget: 'textarea', label: 'Rationale', required: true },
      ],
    },
  },
  {
    name: 'checklist',
    label: 'Checklist',
    description: 'Tick what was checked, comment what was not.',
    form: {
      submit: 'Done',
      fields: [
        { name: 'checked', widget: 'multiselect', label: 'Checked', constraints: { options: ['item 1', 'item 2', 'item 3'] } },
        { name: 'comment', widget: 'textarea', label: 'Comment' },
      ],
    },
  },
];

/** The form with the fields of a pattern added at index, renamed when their names are taken (references follow). */
export function applyPattern(form: FormDef, pattern: Pattern, index = fieldsOf(form).length): FormDef {
  let out = form;
  const renames = new Map<string, string>();
  const added: FieldDef[] = [];
  for (const f of pattern.form.fields ?? []) {
    const name = uniqueName({ fields: [...fieldsOf(out), ...added] }, f.name);
    renames.set(f.name, name);
    added.push(structuredClone({ ...f, name }));
  }
  for (const f of added) {
    const from = f.constraints?.['from'];
    if (typeof from === 'string' && renames.has(from)) f.constraints = { ...f.constraints, from: renames.get(from) };
  }
  added.forEach((f, i) => (out = insertField(out, f, index + i)));
  if (!out.submit && pattern.form.submit) out = { ...out, submit: pattern.form.submit };
  return out;
}
