// The declared forms on screen, offered to the assistant (ADR 0115, ADR 0092): each mounted renderer that names itself
// is a `form` entity of the screen (its fields and what their values look like) and can be filled (`fill_form`, an
// effect: the person still submits) or submitted (`submit_form`, a proposal the person accepts). Every renderer
// registers the same two tools: whichever the registry keeps dispatches to the form named by the call.
import { registerAssist, type ToolArgs } from '../assist/registry.svelte';
import { activeTab } from '../shell/tabs.svelte';
import type { FieldDef, FormDef, FormValues } from './types';
import { DISPLAY_WIDGETS, strings } from './validate';

export interface FormHandle {
  id: string;
  label: string;
  form: () => FormDef;
  /** the issues of the values it holds, by field */
  issues: () => Record<string, string[]>;
  values: () => FormValues;
  /** merges values in (unknown fields refused by the caller) */
  fill: (values: FormValues) => void;
  /** submits what it holds; an error text when it cannot */
  submit: () => string | undefined;
}

interface Live extends FormHandle {
  tab: string;
}

const live = new Map<string, Live>();

/** What a field's value looks like, for the model: its widget and what limits it. */
export function fieldShape(f: FieldDef): string {
  const c = f.constraints ?? {};
  const req = f.required ? '*' : '';
  switch (f.widget) {
    case 'select':
      return `${f.name}${req}: one of ${strings(c['options']).join('|')}`;
    case 'multiselect':
      return `${f.name}${req}: list of ${strings(c['options']).join('|')}`;
    case 'node':
      return `${f.name}${req}: ${c['multiple'] ? 'list of node keys' : 'a node key'}${strings(c['types']).length ? ` of ${strings(c['types']).join('|')}` : ''}`;
    case 'link':
      return `${f.name}${req}: list of {to: node key${strings(c['linkTypes']).length > 1 ? ', type' : ''}}`;
    case 'newNode':
      return `${f.name}${req}: {type, key, props}${strings(c['types']).length ? ` type ${strings(c['types']).join('|')}` : ''}`;
    case 'decision':
      return `${f.name}${req}: {point, option, justification}`;
    case 'group':
      return `${f.name}${req}: list of {${(f.fields ?? []).map((x) => fieldShape(x)).join('; ')}}`;
    case 'file':
      return `${f.name}${req}: a file (the person attaches it)`;
    default:
      return `${f.name}${req}: ${f.widget}`;
  }
}

function entity(h: Live) {
  const fields = (h.form().fields ?? []).filter((f) => !DISPLAY_WIDGETS.has(f.widget));
  const issues = h.issues();
  const n = Object.values(issues).reduce((k, l) => k + l.length, 0);
  return {
    type: 'form',
    id: h.id,
    label: h.label,
    props: {
      fields: fields.map(fieldShape).join('; ').slice(0, 1500),
      filled: fields.filter((f) => h.values()[f.name] !== undefined).map((f) => f.name).join(',') || 'none',
      issues: n ? Object.entries(issues).map(([k, l]) => `${k}: ${l.join(', ')}`).join('; ').slice(0, 400) : 'none',
    },
  };
}

const pick = (a: ToolArgs, tab: string): Live | string => {
  const h = live.get(String(a.form ?? ''));
  return h && h.tab === tab ? h : `No form “${String(a.form ?? '')}” on the screen.`;
};

/** Offers a form to the assistant while it is mounted; returns the cleanup. */
export function offerForm(h: FormHandle): () => void {
  const tab = activeTab()?.id ?? '';
  const entry: Live = { ...h, tab };
  live.set(h.id, entry);
  const off = registerAssist({
    tab,
    screen: () => ({ entities: [entity(entry)] }),
    tools: [
      {
        name: 'fill_form',
        targetOf: (a) => `form:${String(a.form ?? '')}`,
        run: (a) => {
          const f = pick(a, tab);
          if (typeof f === 'string') return f;
          let values: unknown;
          try {
            values = typeof a.values === 'string' ? JSON.parse(a.values) : a.values;
          } catch {
            return 'values is not JSON: give a JSON object by field name.';
          }
          if (!values || typeof values !== 'object' || Array.isArray(values)) return 'values is a JSON object by field name.';
          const names = new Set((f.form().fields ?? []).filter((x) => !DISPLAY_WIDGETS.has(x.widget) && x.widget !== 'file').map((x) => x.name));
          const unknown = Object.keys(values).filter((k) => !names.has(k));
          if (unknown.length) return `Not fields of the form: ${unknown.join(', ')} (fields: ${[...names].join(', ')}).`;
          f.fill(values as FormValues);
        },
      },
      {
        name: 'submit_form',
        targetOf: (a) => `form:${String(a.form ?? '')}`,
        run: (a) => {
          const f = pick(a, tab);
          return typeof f === 'string' ? f : f.submit();
        },
      },
    ],
  });
  return () => {
    if (live.get(h.id) === entry) live.delete(h.id);
    off();
  };
}
