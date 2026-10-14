// The form of a node type (ADR 0115 §7): what a person fills in to create a node of the type. Its fields are attributes
// of the type, links starting from the node (`fromKey: $self`) or explanations; this turns the values into the write
// of the node. Pure.
import type { FormDef, FormValues, LinkValue } from './types';
import { DISPLAY_WIDGETS, submittedValues } from './validate';

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
