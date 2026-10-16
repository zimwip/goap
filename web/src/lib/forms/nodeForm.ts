// The form of a node type (ADR 0115 §7): what a person fills in to create a node of the type. Its fields are attributes
// of the type, links starting from the node (`fromKey: $self`) or explanations; this turns the values into the write
// of the node. Pure.
import type { FormDef, FormValues, LinkValue } from './types';
import { DISPLAY_WIDGETS, submittedValues, visibleState } from './validate';

/** The fixed source naming the node the form creates (pkg/form.SelfKey). */
export const SELF_KEY = '$self';

/** What a creation form gives the node: the properties of its attribute fields, the links of its link fields. */
export function nodeWrite(form: FormDef, values: FormValues): { props: Record<string, unknown>; links: { type: string; to: string }[] } {
  const sent = submittedValues(form, values);
  const props: Record<string, unknown> = {};
  const links: { type: string; to: string }[] = [];
  for (const f of form.fields ?? []) {
    if (DISPLAY_WIDGETS.has(f.widget) || !(f.name in sent)) continue;
    if (f.widget === 'link') {
      for (const l of sent[f.name] as LinkValue[]) if (l.from === SELF_KEY && l.type && l.to) links.push({ type: l.type, to: l.to });
    } else props[f.name] = sent[f.name];
  }
  return { props, links };
}

/** An outgoing link of the node a form edits: its id, type and the key of its target. */
export interface CurrentLink {
  id: string;
  type: string;
  to: string;
}

const linkFields = (form: FormDef) => (form.fields ?? []).filter((f) => f.widget === 'link');
const typesOf = (f: { constraints?: Record<string, unknown> }) => (Array.isArray(f.constraints?.['linkTypes']) ? (f.constraints['linkTypes'] as string[]) : []);

/** The current links each link field of the form stands for: those of its link types, then the others to the fields
 * naming none. */
function claimed(form: FormDef, links: CurrentLink[]): Map<string, CurrentLink[]> {
  const out = new Map<string, CurrentLink[]>();
  const left = new Set(links);
  const fields = linkFields(form);
  for (const f of [...fields.filter((f) => typesOf(f).length), ...fields.filter((f) => !typesOf(f).length)]) {
    const types = typesOf(f);
    const mine = [...left].filter((l) => !types.length || types.includes(l.type));
    mine.forEach((l) => left.delete(l));
    out.set(f.name, mine);
  }
  return out;
}

/** The values a node type form starts from to edit an existing node (ADR 0115 §10): its properties and its links. */
export function nodeFormValues(form: FormDef, props: Record<string, unknown>, links: CurrentLink[]): FormValues {
  const out: FormValues = {};
  const byField = claimed(form, links);
  for (const f of form.fields ?? []) {
    if (DISPLAY_WIDGETS.has(f.widget)) continue;
    if (f.widget === 'link') out[f.name] = (byField.get(f.name) ?? []).map((l): LinkValue => ({ from: SELF_KEY, type: l.type, to: l.to }));
    else if (props[f.name] !== undefined && props[f.name] !== null) out[f.name] = props[f.name];
  }
  return out;
}

/** What an edit through the node type form changes: the properties that differ, the links to add and those to remove
 * (by id). A string property left blank is cleared; what the form does not show, or hides, is kept. */
export function nodeEdit(
  form: FormDef,
  values: FormValues,
  props: Record<string, unknown>,
  links: CurrentLink[],
): { props: Record<string, unknown>; add: { type: string; to: string }[]; remove: string[] } {
  const sent = submittedValues(form, values);
  const patch: Record<string, unknown> = {};
  const add: { type: string; to: string }[] = [];
  const remove: string[] = [];
  const byField = claimed(form, links);
  const { visible } = visibleState(form.fields, values);
  for (const f of form.fields ?? []) {
    if (DISPLAY_WIDGETS.has(f.widget) || !visible.has(f.name)) continue;
    if (f.widget === 'link') {
      const want = ((sent[f.name] as LinkValue[] | undefined) ?? []).filter((l) => l.from === SELF_KEY && l.type && l.to);
      const have = byField.get(f.name) ?? [];
      for (const l of want) if (!have.some((h) => h.type === l.type && h.to === l.to) && !add.some((a) => a.type === l.type && a.to === l.to)) add.push({ type: l.type!, to: l.to! });
      for (const h of have) if (!want.some((l) => l.type === h.type && l.to === h.to)) remove.push(h.id);
      continue;
    }
    const before = props[f.name];
    if (f.name in sent) {
      if (JSON.stringify(sent[f.name]) !== JSON.stringify(before)) patch[f.name] = sent[f.name];
    } else if (typeof before === 'string' && before !== '') patch[f.name] = '';
  }
  return { props: patch, add, remove };
}
