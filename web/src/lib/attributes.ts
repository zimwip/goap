// Attributes of a node type (the registry's definition, resolved by the catalogue): how a node's
// properties are laid out, edited and shown.
import type { AttributeInfo } from './api';
import { parseCode } from './codeText';
import { defaultDiff, type DiffMode } from './valueDiff';

export interface AttributeView {
  name: string;
  label: string;
  type: string;
  widget: string;
  /** how a change of its value is shown (the attribute's, else the usual one of its type) */
  diff: DiffMode;
  tooltip: string;
  section: string;
  asName: boolean;
  default: string;
  values: { value: string; label: string }[];
  enum: string;
  /** names of the property validators that check the value (read only for the users of the node) */
  validators: string[];
}

const WIDGETS: Record<string, string> = { enum: 'dropdown', boolean: 'checkbox', date: 'date', code: 'code' };

export function attributeView(a: AttributeInfo): AttributeView {
  const at = a.attribute ?? {};
  const type = at.type ?? '';
  return {
    name: at.name ?? '',
    label: at.label || at.name || '',
    type,
    widget: at.widget || WIDGETS[type] || 'text',
    diff: defaultDiff(at),
    tooltip: at.tooltip ?? '',
    section: at.section ?? '',
    asName: at.asName === true,
    default: at.defaultValue ?? '',
    values: (a.values ?? []).map((v) => ({ value: v.value ?? '', label: v.label || v.value || '' })),
    enum: at.enum ?? '',
    validators: at.validators ?? [],
  };
}

/** The attributes sorted by section (in order of first appearance) then by order, as the editors lay them out. */
export function orderedAttributes(infos: AttributeInfo[]): AttributeView[] {
  const items = infos.map((a, i) => ({ v: attributeView(a), order: a.attribute?.order ?? 0, i }));
  const sections: string[] = [];
  for (const it of items) if (!sections.includes(it.v.section)) sections.push(it.v.section);
  items.sort((a, b) => sections.indexOf(a.v.section) - sections.indexOf(b.v.section) || a.order - b.order || a.i - b.i);
  return items.map((it) => it.v);
}

/** The attribute of that name among the attributes of a type (undefined: the type declares none). */
export function attributeOf(infos: AttributeInfo[], name: string | undefined): AttributeView | undefined {
  const a = name ? infos.find((x) => x.attribute?.name === name) : undefined;
  return a ? attributeView(a) : undefined;
}

/** The name of a node: the value of its asName attribute, when it has one. */
export function displayName(infos: AttributeInfo[], props: Record<string, unknown>): string {
  const a = infos.find((x) => x.attribute?.asName);
  const v = a ? props[a.attribute?.name ?? ''] : undefined;
  return typeof v === 'string' ? v : '';
}

export const valueText = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));

/** What a value looks like to the user: an enum shows its label. */
export function shownValue(a: AttributeView | undefined, v: unknown): string {
  const t = valueText(v);
  if (!a || !t) return t;
  if (a.type === 'enum') return a.values.find((x) => x.value === t)?.label ?? t;
  if (a.type === 'boolean') return v === true || t === 'true' ? 'yes' : 'no';
  return t;
}

/** Is the attribute an address, shown as a link opened the way its scheme says (ADR 0171, address.ts)? */
export const isUrl = (a: AttributeView | undefined): boolean => !!a && a.widget === 'url';

/** Is the attribute source text, shown and edited as code (codeText.ts)? */
export const isCode = (a: AttributeView | undefined): boolean => !!a && (a.type === 'code' || a.widget === 'code');

/** The value typed in the form, converted to what the type of the attribute holds. An empty entry stays empty. */
export function parseValue(a: AttributeView, raw: string): unknown {
  if (raw === '') return '';
  switch (a.type) {
    case 'number': {
      const n = Number(raw);
      if (Number.isNaN(n)) throw new Error(`“${a.label}” expects a number, got ${raw}`);
      return n;
    }
    case 'boolean':
      return raw === 'true';
    case 'json':
      try {
        return JSON.parse(raw);
      } catch {
        throw new Error(`“${a.label}” expects JSON: ${raw} is not valid`);
      }
    case 'code':
      try {
        parseCode(raw);
      } catch (e) {
        throw new Error(`“${a.label}” expects a code text: ${e instanceof Error ? e.message : String(e)}`);
      }
  }
  return raw;
}
